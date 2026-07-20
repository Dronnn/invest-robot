package paper

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/execution"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// Simulator is the paper-trading Executor. It owns no in-memory market state:
// intents, fills, events, the trading-session window and per-instrument trading
// status all live in SQLite, so it is safe to feed quotes on one goroutine
// while the cycle submits on another (SQLite serializes the writes), and every
// gate a fill depends on survives a restart.
type Simulator struct {
	db      *sqlite.DB
	clock   clock.Clock
	journal *execution.Journal
	applier execution.FillApplier

	slippageBps int64
	commNum     int64 // commission rate as an exact ratio commNum/commDen …
	commDen     int64 // … so a fee is notional*commNum/commDen with no float.
	maxQuoteAge time.Duration

	cashFloor model.Decimal // operational-halt floor; a fill settling cash below it latches the halt (zero disables)
	currency  string        // account settlement currency, used to read the cash balance for the floor check
}

// New builds a paper Simulator. cfg supplies the slippage and commission rate;
// maxQuoteAge is how old a quote may be and still fill an order (a
// non-positive value disables the freshness gate). applier is the portfolio
// hook invoked inside each fill's transaction. cashFloor and currency configure
// the post-fill operational-halt check: a fill that settles the account's cash
// (in currency) below cashFloor latches the durable halt so risk blocks new
// buys; a non-positive cashFloor disables the check.
func New(db *sqlite.DB, clk clock.Clock, applier execution.FillApplier, cfg config.PaperConfig, maxQuoteAge time.Duration, cashFloor model.Decimal, currency string) (*Simulator, error) {
	if db == nil {
		return nil, fmt.Errorf("paper: nil db")
	}
	if clk == nil {
		return nil, fmt.Errorf("paper: nil clock")
	}
	if applier == nil {
		return nil, fmt.Errorf("paper: nil fill applier")
	}
	if cfg.SlippageBps < 0 {
		return nil, fmt.Errorf("paper: negative slippage_bps %d", cfg.SlippageBps)
	}
	num, den, err := parseRate(cfg.CommissionRate)
	if err != nil {
		return nil, err
	}
	return &Simulator{
		db:          db,
		clock:       clk,
		journal:     execution.NewJournal(clk),
		applier:     applier,
		slippageBps: int64(cfg.SlippageBps),
		commNum:     num,
		commDen:     den,
		maxQuoteAge: maxQuoteAge,
		cashFloor:   cashFloor,
		currency:    currency,
	}, nil
}

// Submit journals an intent for each actionable decision and drives it to the
// resting `acked` state. It never fills: a market order fills on the next
// OnQuote, a limit order rests until a quote crosses it (DESIGN §7). The current
// session window and each instrument's trading status are persisted first, so
// OnQuote gates fills on them even after a restart, before another Submit runs.
func (s *Simulator) Submit(ctx context.Context, ds []model.Decision, sc execution.SubmitContext) error {
	if len(sc.DecisionIDs) != len(ds) {
		return fmt.Errorf("paper: submit got %d decisions but %d decision ids", len(ds), len(sc.DecisionIDs))
	}
	now := s.clock.Now()
	if err := (sqlite.ExecSessionRepo{}).Upsert(ctx, s.db,
		sqlite.ExecSession{Start: sc.Session.Start, End: sc.Session.End}, now); err != nil {
		return err
	}
	for uid, ic := range sc.Instruments {
		if ic.TradingStatus == "" {
			continue // not provided: leave the instrument unrestricted
		}
		if err := (sqlite.TradingStatusRepo{}).Upsert(ctx, s.db, sqlite.TradingStatus{
			InstrumentUID: uid,
			Status:        ic.TradingStatus,
			BuyAvailable:  ic.BuyAvailable,
			SellAvailable: ic.SellAvailable,
		}, now); err != nil {
			return err
		}
	}
	for i, d := range ds {
		if err := s.submitOne(ctx, d, sc.DecisionIDs[i], sc, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Simulator) submitOne(ctx context.Context, d model.Decision, decisionID int64, sc execution.SubmitContext, now time.Time) error {
	side, actionable := sideForAction(d.Action)
	if !actionable {
		if d.Action == model.ActionClose {
			// close carries no side or quantity (internal/decision validates it
			// that way) and sizing it needs the position, which execution does
			// not own (DESIGN §3). The cycle resolves close->sell/buy before
			// Submit; a close reaching here is a wiring gap, not an order.
			return s.event(ctx, s.db, "close_unresolved",
				map[string]string{"instrument_uid": string(d.InstrumentUID), "reason": "close reached executor unresolved; skipped"}, now)
		}
		return nil // hold: no order
	}
	ic, known := sc.Instruments[d.InstrumentUID]
	if !known {
		return s.event(ctx, s.db, "no_exec_context",
			map[string]string{"instrument_uid": string(d.InstrumentUID), "reason": "no instrument context for decision; skipped"}, now)
	}
	if d.Quantity <= 0 {
		// The order_intents schema forbids qty <= 0, so such a decision can
		// never be journaled; record and skip rather than fail the batch.
		return s.event(ctx, s.db, "invalid_quantity",
			map[string]string{"instrument_uid": string(d.InstrumentUID), "reason": "decision quantity not positive; skipped"}, now)
	}

	// Journal and drive the intent all the way to acked (or rejected) in one
	// transaction (DESIGN §4). A crash mid-flight rolls the whole thing back, so
	// an intent is never left stranded in new/submitted — a state quote
	// processing never loads and so would leave the order stuck forever.
	return sqlite.WithTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
		// Idempotency: a prior (possibly interrupted) Submit may already have
		// journaled this decision. Retrying the batch must reuse that intent,
		// not mint a second order under a new client order id.
		if _, exists, err := (sqlite.IntentRepo{}).FindByDecision(ctx, tx, decisionID); err != nil {
			return err
		} else if exists {
			return nil
		}

		in, err := s.journal.Open(ctx, tx, execution.NewIntent{
			DecisionID:    decisionID,
			InstrumentUID: d.InstrumentUID,
			Side:          side,
			Qty:           d.Quantity,
			Type:          d.OrderType,
			LimitPrice:    d.LimitPrice,
			TimeInForce:   d.TimeInForce,
		})
		if err != nil {
			return err
		}

		// Validate the instrument data now that the intent is journaled. Bad
		// data is a rejection recorded on the intent (new->rejected), committed
		// in this same transaction, not a batch error.
		if ic.Instrument.MinPriceIncrement.Sign() <= 0 {
			return s.rejectTx(ctx, tx, in.ClientOrderID, model.IntentNew, "unknown or invalid price tick", now)
		}
		if d.OrderType == model.OrderLimit && (d.LimitPrice == nil || d.LimitPrice.Sign() <= 0) {
			return s.rejectTx(ctx, tx, in.ClientOrderID, model.IntentNew, "limit order without a valid limit price", now)
		}

		// new -> submitted -> acked via CAS. The order now rests; it can only
		// fill on a later OnQuote.
		if err := s.journal.Transition(ctx, tx, in.ClientOrderID, model.IntentNew, model.IntentSubmitted); err != nil {
			return err
		}
		return s.journal.Transition(ctx, tx, in.ClientOrderID, model.IntentSubmitted, model.IntentAcked)
	})
}

// Recover brings the durable intent journal back to a consistent state after a
// restart. Paper submission drives new->submitted->acked in a single
// transaction, so any intent still in new or submitted is a remnant of an
// interrupted (or pre-atomic) submission that never became a resting order —
// and OnQuote only ever fills acked intents, so it would otherwise sit forever.
// Recover moves each to a terminal, reconcilable state: a never-submitted new
// intent to rejected, a submitted-but-unacked intent to canceled. acked intents
// are left untouched — they are legitimately resting and fill on the next
// observation. Recover is idempotent and safe to call on every startup; a
// concurrent transition (StateConflictError) is skipped, not treated as an
// error. Call it once before the first OnQuote.
func (s *Simulator) Recover(ctx context.Context) error {
	nonTerminal, err := (sqlite.IntentRepo{}).NonTerminal(ctx, s.db)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	for _, in := range nonTerminal {
		var to model.IntentState
		switch in.State {
		case model.IntentNew:
			to = model.IntentRejected
		case model.IntentSubmitted:
			to = model.IntentCanceled
		default:
			continue // acked (resting) or unknown: leave for normal processing
		}
		from := in.State
		err := sqlite.WithTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
			if err := s.journal.TransitionWithReason(ctx, tx, in.ClientOrderID, from, to, "recovered: incomplete submission resolved on startup"); err != nil {
				return err
			}
			return s.event(ctx, tx, "order_recovered", map[string]string{
				"client_order_id": in.ClientOrderID,
				"from":            from.String(),
				"to":              to.String(),
				"reason":          "incomplete submission resolved on startup",
			}, now)
		})
		var conflict sqlite.StateConflictError
		if errors.As(err, &conflict) {
			continue // moved under us between the read and the CAS
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// OnQuote offers q to every order resting on q's instrument. Each fillable order
// settles in its own transaction; a non-fillable one rests (day) or is canceled
// (ioc). It is safe to call concurrently with Submit.
func (s *Simulator) OnQuote(ctx context.Context, q model.Quote) error {
	resting, err := s.restingIntents(ctx, q.InstrumentUID)
	if err != nil {
		return err
	}
	if len(resting) == 0 {
		return nil
	}
	instr, err := (sqlite.InstrumentRepo{}).Get(ctx, s.db, q.InstrumentUID)
	if errors.Is(err, sqlite.ErrNotFound) {
		// No instrument metadata: nothing can be priced, orders keep resting.
		return nil
	}
	if err != nil {
		return err
	}

	now := s.clock.Now()
	sess, err := s.currentSession(ctx)
	if err != nil {
		return err
	}
	status, hasStatus, err := (sqlite.TradingStatusRepo{}).Get(ctx, s.db, q.InstrumentUID)
	if err != nil {
		return err
	}
	for _, in := range resting {
		if err := s.tryFill(ctx, in, q, instr, now, sess, status, hasStatus); err != nil {
			return err
		}
	}
	return nil
}

// currentSession reads the persisted trading-session window. A missing row is
// the documented 24-hour-open default (a zero-value Session), so an OnQuote
// before the first Submit — or against a fresh database — is unrestricted.
func (s *Simulator) currentSession(ctx context.Context) (execution.Session, error) {
	sess, found, err := (sqlite.ExecSessionRepo{}).Get(ctx, s.db)
	if err != nil {
		return execution.Session{}, err
	}
	if !found {
		return execution.Session{}, nil
	}
	return execution.Session{Start: sess.Start, End: sess.End}, nil
}

// tryFill attempts to fill one resting intent against q, enforcing the
// next-observation discipline, freshness, session, tick and (for limits) the
// crossing condition. An order that cannot fill on this observation rests if it
// is a day order or is canceled if it is immediate-or-cancel.
//
// Next-observation discipline (DESIGN §7): an order may only fill on an
// observation taken strictly after it was activated. The intent's CreatedAt is
// the activation instant — the moment Submit journaled it — so a quote at or
// before it is the same or an earlier observation than the one the decision was
// made on and must not fill. A future-dated quote (TS after the current clock)
// is likewise not a real observation yet. Crucially, neither a pre-activation
// nor a future quote may cancel an immediate-or-cancel order: those quotes are
// simply not this order's next observation, so it keeps resting until a real
// later one arrives (a delayed pre-decision quote must neither fill nor cancel).
//
// When a persisted trading status is present (hasStatus), the order's side must
// be available on it: a suspended or side-disabled instrument does not fill. An
// absent status leaves the instrument unrestricted.
func (s *Simulator) tryFill(ctx context.Context, in model.OrderIntent, q model.Quote, instr model.Instrument, now time.Time, sess execution.Session, status sqlite.TradingStatus, hasStatus bool) error {
	if q.TS.After(now) {
		return nil // future-dated observation: ignore, keep resting
	}
	if !q.TS.After(in.CreatedAt) {
		return nil // at or before activation: not this order's next observation
	}

	rest := func(reason string) error {
		if in.TimeInForce == model.TIFIOC {
			return s.cancel(ctx, in.ClientOrderID, "ioc unfilled: "+reason, now)
		}
		return nil
	}

	if s.maxQuoteAge > 0 && now.Sub(q.TS) > s.maxQuoteAge {
		return rest("stale quote")
	}
	if !sess.IsOpen(q.TS) {
		return rest("outside trading session")
	}
	if hasStatus && !sideAvailable(in.Side, status) {
		return rest("instrument not available for " + in.Side.String() + " (status " + status.Status + ")")
	}
	if instr.MinPriceIncrement.Sign() <= 0 {
		return rest("unknown price tick")
	}

	price, lowFidelity, ok, err := s.priceFill(in.Side, in.Type, in.LimitPrice, q, instr.MinPriceIncrement)
	if err != nil {
		return err
	}
	if !ok {
		return rest("not marketable")
	}
	return s.settle(ctx, in, price, lowFidelity, instr, now)
}

// ExpireDay cancels every resting day-TIF intent at the close of the session
// ending at sessionEnd. Immediate-or-cancel orders never rest across
// observations, so only day orders are affected. A concurrent fill or cancel
// (StateConflictError) is skipped, not treated as an error.
func (s *Simulator) ExpireDay(ctx context.Context, sessionEnd time.Time) error {
	nonTerminal, err := (sqlite.IntentRepo{}).NonTerminal(ctx, s.db)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	for _, in := range nonTerminal {
		if in.State != model.IntentAcked || in.TimeInForce != model.TIFDay {
			continue
		}
		err := sqlite.WithTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
			if err := s.journal.TransitionWithReason(ctx, tx, in.ClientOrderID, model.IntentAcked, model.IntentCanceled, "day order expired"); err != nil {
				return err
			}
			return s.event(ctx, tx, "order_expired", map[string]string{
				"client_order_id": in.ClientOrderID,
				"session_end":     sessionEnd.UTC().Format(time.RFC3339),
				"reason":          "day order expired at session end",
			}, now)
		})
		var conflict sqlite.StateConflictError
		if errors.As(err, &conflict) {
			continue // moved under us between the read and the CAS
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// settle commits one fill atomically: the intent's acked->filled CAS, the fills
// row, and the portfolio application all live in one transaction, so any error
// (including a CAS conflict from a concurrent fill, or a failing applier) rolls
// the whole thing back and leaves the intent resting in acked.
func (s *Simulator) settle(ctx context.Context, in model.OrderIntent, price model.Decimal, lowFidelity bool, instr model.Instrument, now time.Time) error {
	fee, err := commission(price, in.Qty, instr.Lot, s.commNum, s.commDen)
	if err != nil {
		return err
	}
	fill := model.Fill{IntentID: in.ClientOrderID, Price: price, Qty: in.Qty, Fee: fee, TS: now}
	fa := execution.FillApplication{
		Fill:          fill,
		InstrumentUID: in.InstrumentUID,
		Side:          in.Side,
		Lot:           instr.Lot,
		Currency:      instr.Currency,
		LowFidelity:   lowFidelity,
	}
	return sqlite.WithTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
		if err := s.journal.Transition(ctx, tx, in.ClientOrderID, model.IntentAcked, model.IntentFilled); err != nil {
			return err
		}
		if err := (sqlite.FillRepo{}).Insert(ctx, tx, fill, lowFidelity); err != nil {
			return err
		}
		if err := s.applier.ApplyFill(ctx, tx, fa); err != nil {
			return err
		}
		return s.enforceCashFloor(ctx, tx, now)
	})
}

// enforceCashFloor latches the operational halt when the account's cash has
// settled below the configured floor after a fill. A real fill can never be
// refused once it has occurred, so the fill stands; instead the durable halt is
// engaged in the same transaction, and risk then blocks all new buys until an
// operator clears it. A non-positive cashFloor disables the check.
func (s *Simulator) enforceCashFloor(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if s.cashFloor.Sign() <= 0 {
		return nil
	}
	bal, err := (sqlite.CashRepo{}).Balance(ctx, tx, s.currency)
	if err != nil {
		return err
	}
	if bal.Cmp(s.cashFloor) >= 0 {
		return nil
	}
	if err := (sqlite.HaltRepo{}).Engage(ctx, tx, "cash settled below the configured floor after a fill", now); err != nil {
		return err
	}
	return s.event(ctx, tx, "operational_halt", map[string]string{
		"reason": "cash settled below the configured floor after a fill",
		"floor":  s.cashFloor.String(),
		"cash":   bal.String(),
	}, now)
}

// rejectTx moves a journaled intent to the terminal rejected state within the
// caller's transaction, recording the human-readable reason both on the intent
// row (order_intents.reason) and as an event (DESIGN §12's durable log). It
// takes the transaction rather than opening its own so a submission's journal,
// validation and rejection all commit or roll back together.
func (s *Simulator) rejectTx(ctx context.Context, tx sqlite.Querier, clientOrderID string, from model.IntentState, reason string, now time.Time) error {
	if err := s.journal.TransitionWithReason(ctx, tx, clientOrderID, from, model.IntentRejected, reason); err != nil {
		return err
	}
	return s.event(ctx, tx, "order_rejected", map[string]string{
		"client_order_id": clientOrderID,
		"reason":          reason,
	}, now)
}

// cancel moves a resting intent to canceled and records why, persisting the
// reason on the intent row (order_intents.reason) as well as in the events log.
// Used for immediate-or-cancel orders that did not fill on their first genuine
// observation.
func (s *Simulator) cancel(ctx context.Context, clientOrderID string, reason string, now time.Time) error {
	return sqlite.WithTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
		if err := s.journal.TransitionWithReason(ctx, tx, clientOrderID, model.IntentAcked, model.IntentCanceled, reason); err != nil {
			return err
		}
		return s.event(ctx, tx, "order_canceled", map[string]string{
			"client_order_id": clientOrderID,
			"reason":          reason,
		}, now)
	})
}

// event appends a structured events row. The payload is a sorted-key JSON object
// (encoding/json over a map), so identical inputs produce identical bytes.
func (s *Simulator) event(ctx context.Context, q sqlite.Querier, code string, fields map[string]string, now time.Time) error {
	payload, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("paper: marshal event payload: %w", err)
	}
	if _, err := (sqlite.EventRepo{}).Insert(ctx, q, sqlite.Event{
		TS:      now,
		Level:   "warn",
		Code:    code,
		Payload: string(payload),
	}); err != nil {
		return err
	}
	return nil
}

// restingIntents returns the acked intents for uid, oldest first (NonTerminal's
// order), which is the deterministic order OnQuote fills them in.
func (s *Simulator) restingIntents(ctx context.Context, uid model.InstrumentUID) ([]model.OrderIntent, error) {
	nonTerminal, err := (sqlite.IntentRepo{}).NonTerminal(ctx, s.db)
	if err != nil {
		return nil, err
	}
	var out []model.OrderIntent
	for _, in := range nonTerminal {
		if in.State == model.IntentAcked && in.InstrumentUID == uid {
			out = append(out, in)
		}
	}
	return out, nil
}

// sideAvailable reports whether an order side is permitted by a persisted
// trading status: a buy needs BuyAvailable, a sell needs SellAvailable. A
// suspended instrument has both false.
func sideAvailable(side model.Side, status sqlite.TradingStatus) bool {
	switch side {
	case model.SideSell:
		return status.SellAvailable
	default:
		return status.BuyAvailable
	}
}

// sideForAction maps a decision action to an order side. Only buy and sell are
// directly actionable at execution; hold is a no-op and close is resolved
// upstream (see submitOne).
func sideForAction(a model.Action) (model.Side, bool) {
	switch a {
	case model.ActionBuy:
		return model.SideBuy, true
	case model.ActionSell:
		return model.SideSell, true
	default:
		return "", false
	}
}
