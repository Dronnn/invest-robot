package cycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Dronnn/invest-robot/internal/decision"
	"github.com/Dronnn/invest-robot/internal/execution"
	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/portfolio"
	"github.com/Dronnn/invest-robot/internal/risk"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// assembled carries the side outputs of the assemble step that later steps
// reuse: the priced quotes, instrument metadata, and the session window.
type assembled struct {
	quotes  map[model.InstrumentUID]model.Quote
	instr   map[model.InstrumentUID]model.Instrument
	session execution.Session
}

// RunOnce runs one full cycle through the state machine and returns its summary.
// It never fills orders — the paper executor rests them for the next quote — and
// never panics the caller on a data problem: a stale or empty cycle is recorded
// and skipped (DESIGN §6, §12).
func (e *Engine) RunOnce(ctx context.Context) (Summary, error) {
	now := e.deps.Clock.Now()
	asOf := now

	cycleID, err := (sqlite.CycleRepo{}).Insert(ctx, e.deps.DB, sqlite.Cycle{
		StartedAt:      now,
		AsOf:           asOf,
		Mode:           e.cfg.Mode,
		Engine:         e.deps.Engine.Name(),
		EngineVersion:  e.cfg.EngineVersion,
		ConfigSnapshot: e.cfg.ConfigSnapshot,
		Status:         "running",
	})
	if err != nil {
		return Summary{}, fmt.Errorf("cycle: insert cycle: %w", err)
	}

	// --- assemble ---
	req, asm, ready, err := e.assemble(ctx, cycleID, asOf, now)
	if err != nil {
		return e.fail(ctx, cycleID, now, "assemble", err)
	}
	if !ready {
		_ = (sqlite.CycleRepo{}).UpdateStatus(ctx, e.deps.DB, cycleID, "skipped")
		e.logEvent(ctx, "warn", "cycle_skipped", "no instrument with fresh enough data as of "+asOf.UTC().Format(time.RFC3339))
		s := Summary{ID: cycleID, StartedAt: now, Mode: e.cfg.Mode, Engine: e.deps.Engine.Name(), Status: "skipped"}
		e.setLast(s)
		return s, nil
	}

	// Day-PnL baseline for the kill switch.
	sessionStart := e.dayBaseline(asOf)
	dayPnL := e.dayPnL(ctx, asm.quotes, sessionStart, now)

	// --- decide ---
	dctx, cancel := context.WithTimeout(ctx, e.cfg.DecisionBudget)
	resp, meta, derr := e.deps.Engine.Decide(dctx, req)
	cancel()
	e.persistLLMCall(ctx, cycleID, req, meta, derr, now)
	if derr != nil {
		return e.fail(ctx, cycleID, now, "decide", derr)
	}

	// --- validate ---
	badByIndex := validateActions(resp, req)

	valid := make([]model.Decision, 0, len(resp.Actions))
	validJByOrig := make(map[int]int, len(resp.Actions))
	for i, a := range resp.Actions {
		if _, bad := badByIndex[i]; !bad {
			validJByOrig[i] = len(valid)
			valid = append(valid, a)
		}
	}

	// --- risk-check ---
	state := e.buildRiskState(ctx, req, asm, dayPnL, now)
	result := risk.Check(valid, state, e.cfg.Risk)
	e.persistAdjustments(ctx, cycleID, result.Adjustments, now)
	final := reconstructFinal(len(valid), result.Adjustments)

	// Persist every engine action as a decision row with its final status, and
	// build the order list (resolving close→sell) linked by decision id.
	decisionIDByOrig := make(map[int]int64, len(resp.Actions))
	rejected := 0
	for i, a := range resp.Actions {
		status, dec := "allowed", a
		if ae, bad := badByIndex[i]; bad {
			status = "rejected: " + ae.Field + ": " + ae.Message
			rejected++
		} else {
			f := final[validJByOrig[i]]
			dec = valid[validJByOrig[i]]
			switch {
			case !f.allowed:
				status = "risk_stripped: " + string(f.rule)
			case f.shrunk:
				status = "risk_shrunk: " + string(f.rule)
				dec.Quantity = f.qty
			}
		}
		id, err := (sqlite.DecisionRepo{}).Insert(ctx, e.deps.DB, sqlite.DecisionRecord{
			CycleID: cycleID, Decision: dec, ValidationStatus: status,
		})
		if err != nil {
			return e.fail(ctx, cycleID, now, "persist decision", err)
		}
		decisionIDByOrig[i] = id
	}

	orders, orderIDs := e.buildOrders(ctx, resp, badByIndex, validJByOrig, valid, final, decisionIDByOrig, now)

	// --- execute ---
	if len(orders) > 0 {
		sc := execution.SubmitContext{
			Instruments: e.execInstruments(asm, orders),
			DecisionIDs: orderIDs,
			Session:     asm.session,
		}
		if err := e.deps.Executor.Submit(ctx, orders, sc); err != nil {
			return e.fail(ctx, cycleID, now, "execute", err)
		}
		e.bumpOrdersToday(now, len(orders))
	}

	// --- account ---
	if _, err := e.deps.Portfolio.MarkToMarket(ctx, e.deps.DB, asm.quotes, now); err != nil {
		e.logEvent(ctx, "warn", "mark_to_market_failed", err.Error())
	}

	// --- report ---
	status := "ok"
	if len(resp.Actions) > 0 && rejected == len(resp.Actions) {
		status = "rejected"
	}
	if err := (sqlite.CycleRepo{}).UpdateStatus(ctx, e.deps.DB, cycleID, status); err != nil {
		return Summary{}, fmt.Errorf("cycle: update status: %w", err)
	}
	s := Summary{
		ID: cycleID, StartedAt: now, Mode: e.cfg.Mode, Engine: e.deps.Engine.Name(),
		Status: status, Decisions: len(resp.Actions), Orders: len(orders),
	}
	e.setLast(s)
	return s, nil
}

// assemble builds the decision request strictly as of asOf: completed candles,
// as-of quotes, and per-instrument feature snapshots (persisting each and
// carrying the prior snapshot's EMA trend forward). ready is false when no
// instrument has fresh enough data to decide on.
func (e *Engine) assemble(ctx context.Context, cycleID int64, asOf, now time.Time) (decision.Request, assembled, bool, error) {
	instruments, err := (sqlite.InstrumentRepo{}).List(ctx, e.deps.DB)
	if err != nil {
		return decision.Request{}, assembled{}, false, fmt.Errorf("cycle: list instruments: %w", err)
	}

	quotes := make(map[model.InstrumentUID]model.Quote)
	instrMap := make(map[model.InstrumentUID]model.Instrument)
	var contexts []decision.InstrumentContext
	readyCount := 0

	from := asOf.Add(-time.Duration(e.cfg.LookbackBars) * intervalDuration(e.cfg.Interval))
	for _, m := range instruments {
		instrMap[m.UID] = m

		bars, err := (sqlite.CandleRepo{}).Range(ctx, e.deps.DB, m.UID, e.cfg.Interval, from, asOf)
		if err != nil {
			e.logEvent(ctx, "warn", "candle_read_failed", fmt.Sprintf("%s: %v", m.UID, err))
			continue
		}
		complete := bars[:0]
		for _, b := range bars {
			if b.Complete {
				complete = append(complete, b)
			}
		}
		snap, err := features.Build(m.UID, e.cfg.Interval, complete, e.cfg.FeatureParams)
		if err != nil {
			// Insufficient bars (warm-up) is normal early on; skip the instrument.
			var insuf features.ErrInsufficientData
			if !errors.As(err, &insuf) {
				e.logEvent(ctx, "warn", "feature_build_failed", fmt.Sprintf("%s: %v", m.UID, err))
			}
			continue
		}

		prevTrend := e.prevEMATrend(ctx, m.UID, asOf)
		if _, err := (sqlite.FeatureSnapshotRepo{}).Insert(ctx, e.deps.DB, sqlite.FeatureSnapshot{
			InstrumentUID: m.UID, AsOf: snap.AsOf, Payload: marshalJSON(snap),
		}); err != nil {
			e.logEvent(ctx, "warn", "feature_snapshot_insert_failed", fmt.Sprintf("%s: %v", m.UID, err))
		}

		var quoteView decision.QuoteView
		dataTS := snap.AsOf
		if q, ok, qerr := (sqlite.QuoteRepo{}).LatestAsOf(ctx, e.deps.DB, m.UID, asOf); qerr == nil && ok {
			quotes[m.UID] = q
			quoteView = decision.QuoteView{Bid: q.Bid, Ask: q.Ask, Last: q.Last, TS: q.TS}
			if q.TS.Before(dataTS) {
				dataTS = q.TS
			}
		}

		freshness := asOf.Sub(dataTS)
		if freshness < 0 {
			freshness = 0
		}
		if freshness <= e.cfg.MaxDataAge {
			readyCount++
		}
		contexts = append(contexts, decision.InstrumentContext{
			UID: m.UID, FIGI: m.FIGI, Ticker: m.Ticker, ClassCode: m.ClassCode,
			Lot: m.Lot, MinPriceIncrement: m.MinPriceIncrement,
			Quote: quoteView, Features: snap, PrevEMATrend: prevTrend, DataFreshness: freshness,
		})
	}

	if readyCount == 0 {
		return decision.Request{}, assembled{}, false, nil
	}

	pf := e.portfolioView(ctx, quotes)
	openIntents := e.openIntentViews(ctx)

	req := decision.Request{
		AsOf: asOf, Mode: e.cfg.Mode,
		Cycle:       decision.CycleMeta{ID: cycleID, StartedAt: now, Interval: e.cfg.Interval.String()},
		Portfolio:   pf,
		OpenIntents: openIntents,
		Instruments: contexts,
		Limits:      e.limits(),
	}
	return req, assembled{quotes: quotes, instr: instrMap, session: e.sessionWindow(now)}, true, nil
}

// prevEMATrend reads the EMA trend from the most recent feature snapshot as of
// asOf (the prior cycle's, since the current one is not yet inserted), enabling
// the rules engine's crossover detection. Empty when there is no prior snapshot.
func (e *Engine) prevEMATrend(ctx context.Context, uid model.InstrumentUID, asOf time.Time) features.EMATrend {
	snap, ok, err := (sqlite.FeatureSnapshotRepo{}).LatestAsOf(ctx, e.deps.DB, uid, asOf)
	if err != nil || !ok {
		return ""
	}
	var prev features.Snapshot
	if json.Unmarshal([]byte(snap.Payload), &prev) != nil {
		return ""
	}
	return prev.EMATrend
}

// portfolioView builds the engine's portfolio view, degrading to a cash-only
// view if a held position cannot be priced (DESIGN §12) rather than aborting.
func (e *Engine) portfolioView(ctx context.Context, quotes map[model.InstrumentUID]model.Quote) decision.Portfolio {
	sum, err := e.deps.Portfolio.Summary(ctx, e.deps.DB, quotes)
	if err == nil {
		views := make([]decision.PositionView, 0, len(sum.Positions))
		for _, p := range sum.Positions {
			views = append(views, decision.PositionView{
				UID: p.UID, Qty: p.Qty, AvgPrice: p.AvgPrice, LastPrice: p.LastPrice, UnrealizedPnL: p.UnrealizedPnL,
			})
		}
		return decision.Portfolio{Cash: sum.Cash, Equity: sum.Equity, Positions: views}
	}
	e.logEvent(ctx, "warn", "portfolio_summary_failed", err.Error())
	cash, _ := (sqlite.CashRepo{}).Balance(ctx, e.deps.DB, e.cfg.Currency)
	return decision.Portfolio{Cash: cash, Equity: cash}
}

// openIntentViews returns the non-terminal intents as engine views.
func (e *Engine) openIntentViews(ctx context.Context) []decision.IntentView {
	intents, err := (sqlite.IntentRepo{}).NonTerminal(ctx, e.deps.DB)
	if err != nil {
		e.logEvent(ctx, "warn", "open_intents_read_failed", err.Error())
		return nil
	}
	out := make([]decision.IntentView, 0, len(intents))
	for _, in := range intents {
		out = append(out, decision.IntentView{
			ClientOrderID: in.ClientOrderID, InstrumentUID: in.InstrumentUID, Side: in.Side,
			Qty: in.Qty, Type: in.Type, LimitPrice: in.LimitPrice, TimeInForce: in.TimeInForce,
			State: in.State, CreatedAt: in.CreatedAt,
		})
	}
	return out
}

// buildRiskState assembles the risk snapshot from the request, positions,
// pending intents and quotes.
func (e *Engine) buildRiskState(ctx context.Context, req decision.Request, asm assembled, dayPnL model.Decimal, now time.Time) risk.State {
	halted := false
	if h, err := (sqlite.HaltRepo{}).Status(ctx, e.deps.DB); err == nil {
		halted = h.Engaged
	}

	positions := make(map[model.InstrumentUID]risk.Position)
	if list, err := (sqlite.PositionRepo{}).List(ctx, e.deps.DB); err == nil {
		for _, p := range list {
			if p.Qty == 0 {
				continue
			}
			last := p.AvgPrice
			if q, ok := asm.quotes[p.InstrumentUID]; ok && !q.Last.IsZero() {
				last = q.Last
			}
			positions[p.InstrumentUID] = risk.Position{QtyLots: p.Qty, LastPrice: last, AvgPrice: p.AvgPrice}
		}
	}

	pending := make(map[model.InstrumentUID]risk.PendingIntents)
	for _, in := range req.OpenIntents {
		pi := pending[in.InstrumentUID]
		switch in.Side {
		case model.SideBuy:
			limit := model.Decimal{}
			if in.LimitPrice != nil {
				limit = *in.LimitPrice
			}
			pi.Buys = append(pi.Buys, risk.PendingBuy{Lots: in.Qty, OrderType: in.Type, LimitPrice: limit})
		case model.SideSell:
			pi.SellLots += in.Qty
		}
		pending[in.InstrumentUID] = pi
	}

	return risk.State{
		Halted:            halted,
		BaseCurrency:      e.cfg.Currency,
		Cash:              req.Portfolio.Cash,
		DayPnL:            dayPnL,
		OrdersToday:       e.ordersTodayCount(now),
		Positions:         positions,
		OpenIntents:       pending,
		Quotes:            asm.quotes,
		Instruments:       asm.instr,
		SlippageBufferBps: int64(e.cfg.Paper.SlippageBps),
		FeeBufferBps:      feeBps(e.cfg.Paper.CommissionRate),
	}
}

// buildOrders turns the risk-allowed decisions into orders, resolving close→sell
// against the current position and dropping holds.
func (e *Engine) buildOrders(ctx context.Context, resp decision.Response, badByIndex map[int]decision.ActionError, validJByOrig map[int]int, valid []model.Decision, final []finalState, decisionIDByOrig map[int]int64, now time.Time) ([]model.Decision, []int64) {
	var orders []model.Decision
	var ids []int64
	for i := range resp.Actions {
		if _, bad := badByIndex[i]; bad {
			continue
		}
		f := final[validJByOrig[i]]
		if !f.allowed {
			continue
		}
		dec := valid[validJByOrig[i]]
		if f.shrunk {
			dec.Quantity = f.qty
		}
		switch dec.Action {
		case model.ActionBuy, model.ActionSell:
			orders = append(orders, dec)
			ids = append(ids, decisionIDByOrig[i])
		case model.ActionClose:
			pos, ok, err := (sqlite.PositionRepo{}).Get(ctx, e.deps.DB, dec.InstrumentUID)
			if err != nil || !ok || pos.Qty <= 0 {
				e.logEvent(ctx, "info", "close_no_position",
					"close for "+string(dec.InstrumentUID)+" has no position to flatten")
				continue
			}
			orders = append(orders, model.Decision{
				InstrumentUID: dec.InstrumentUID, Action: model.ActionSell, Quantity: pos.Qty,
				OrderType: model.OrderMarket, TimeInForce: dec.TimeInForce,
				Rationale: "resolved from close: sell full position",
			})
			ids = append(ids, decisionIDByOrig[i])
		}
	}
	return orders, ids
}

// execInstruments builds the per-instrument execution context for the orders.
func (e *Engine) execInstruments(asm assembled, orders []model.Decision) map[model.InstrumentUID]execution.InstrumentContext {
	out := make(map[model.InstrumentUID]execution.InstrumentContext, len(orders))
	for _, o := range orders {
		out[o.InstrumentUID] = execution.InstrumentContext{
			Instrument: asm.instr[o.InstrumentUID],
			Quote:      asm.quotes[o.InstrumentUID],
			// TradingStatus left empty: Phase 1 market data carries normal trading
			// status, so instruments are unrestricted (DESIGN §14 scope).
		}
	}
	return out
}

// dayBaseline is the start of the current session (when in-session) or the UTC
// day, for the DayPnL baseline.
func (e *Engine) dayBaseline(asOf time.Time) time.Time {
	sess := e.sessionWindow(asOf)
	if !sess.Start.IsZero() && sess.IsOpen(asOf) {
		return sess.Start
	}
	return asOf.UTC().Truncate(24 * time.Hour)
}

// dayPnL establishes the session baseline snapshot and returns today's total
// PnL, degrading to zero on any error.
func (e *Engine) dayPnL(ctx context.Context, quotes map[model.InstrumentUID]model.Quote, sessionStart, now time.Time) model.Decimal {
	if err := e.deps.Portfolio.EnsureSessionStartSnapshot(ctx, e.deps.DB, sessionStart); err != nil {
		if errors.Is(err, portfolio.ErrCannotEstablishSessionSnapshot) {
			_, _ = e.deps.Portfolio.MarkToMarket(ctx, e.deps.DB, quotes, now)
			_ = e.deps.Portfolio.EnsureSessionStartSnapshot(ctx, e.deps.DB, sessionStart)
		}
	}
	r, err := e.deps.Portfolio.DayPnL(ctx, e.deps.DB, sessionStart)
	if err != nil {
		return model.Decimal{}
	}
	return r.Total
}

// persistLLMCall records the full request and raw engine output for replay — for
// every engine including rules (DESIGN §5: the llm_calls table is the
// engine-call record, and replay needs the request).
func (e *Engine) persistLLMCall(ctx context.Context, cycleID int64, req decision.Request, meta decision.Meta, decideErr error, now time.Time) {
	errStr := ""
	if decideErr != nil {
		errStr = decideErr.Error()
	}
	if _, err := (sqlite.LLMCallRepo{}).Insert(ctx, e.deps.DB, sqlite.LLMCall{
		CycleID: cycleID, Model: meta.Model, Request: marshalJSON(req),
		Response: string(meta.Raw), DurationMS: meta.DurationMS, Error: errStr, CreatedAt: now,
	}); err != nil {
		e.logEvent(ctx, "warn", "llm_call_insert_failed", err.Error())
	}
}

// persistAdjustments records every risk adjustment as an event.
func (e *Engine) persistAdjustments(ctx context.Context, cycleID int64, adjustments []risk.Adjustment, now time.Time) {
	for _, a := range adjustments {
		payload := marshalJSON(map[string]any{
			"cycle_id":       cycleID,
			"index":          a.Index,
			"instrument_uid": string(a.InstrumentUID),
			"rule":           string(a.Rule),
			"reason":         a.Reason,
			"stripped":       a.Adjusted == nil,
		})
		_, _ = (sqlite.EventRepo{}).Insert(ctx, e.deps.DB, sqlite.Event{
			TS: now, Level: "info", Code: "risk_adjustment", Payload: payload,
		})
	}
}

// fail records a cycle-level failure and returns the error.
func (e *Engine) fail(ctx context.Context, cycleID int64, now time.Time, step string, cause error) (Summary, error) {
	_ = (sqlite.CycleRepo{}).UpdateStatus(ctx, e.deps.DB, cycleID, "error")
	e.logEvent(ctx, "error", "cycle_"+step+"_failed", cause.Error())
	s := Summary{ID: cycleID, StartedAt: now, Mode: e.cfg.Mode, Engine: e.deps.Engine.Name(), Status: "error"}
	e.setLast(s)
	return s, cause
}

// limits maps the config risk limits into the engine's informational Limits.
func (e *Engine) limits() decision.Limits {
	return decision.Limits{
		MaxPositionNotional: parseDec(e.cfg.Risk.MaxPositionNotional),
		MaxTotalExposure:    parseDec(e.cfg.Risk.MaxTotalExposure),
		MaxOrdersPerCycle:   e.cfg.Risk.MaxOrdersPerCycle,
		MaxOrdersPerDay:     e.cfg.Risk.MaxOrdersPerDay,
		MaxDailyLoss:        parseDec(e.cfg.Risk.MaxDailyLoss),
		Allowlist:           e.cfg.Risk.Allowlist,
		CashFloor:           parseDec(e.cfg.Risk.CashFloor),
	}
}

// finalState is a valid action's fate after risk: allowed (optionally shrunk to
// qty) or stripped, and the rule that last touched it.
type finalState struct {
	allowed bool
	shrunk  bool
	qty     int64
	rule    risk.Rule
}

// reconstructFinal derives each valid action's post-risk fate from the ordered
// adjustments: the last adjustment for an index wins (a strip after a shrink is
// a strip; the latest shrink sets the quantity).
func reconstructFinal(n int, adjustments []risk.Adjustment) []finalState {
	out := make([]finalState, n)
	for i := range out {
		out[i] = finalState{allowed: true}
	}
	for _, a := range adjustments {
		if a.Index < 0 || a.Index >= n {
			continue
		}
		if a.Adjusted == nil {
			out[a.Index] = finalState{allowed: false, rule: a.Rule}
		} else {
			out[a.Index] = finalState{allowed: true, shrunk: true, qty: a.Adjusted.Quantity, rule: a.Rule}
		}
	}
	return out
}

// validateActions runs shape then semantic validation and returns the first
// error per action index.
func validateActions(resp decision.Response, req decision.Request) map[int]decision.ActionError {
	bad := make(map[int]decision.ActionError)
	for _, ae := range decision.ValidateShape(resp) {
		if _, seen := bad[ae.Index]; !seen {
			bad[ae.Index] = ae
		}
	}
	for _, ae := range decision.ValidateSemantics(resp, req) {
		if _, seen := bad[ae.Index]; !seen {
			bad[ae.Index] = ae
		}
	}
	return bad
}

func parseDec(s string) model.Decimal {
	d, err := model.ParseDecimal(s)
	if err != nil {
		return model.Decimal{}
	}
	return d
}

// feeBps converts a commission-rate decimal string (e.g. "0.0004") into a
// basis-point figure for the risk cash-floor fee buffer.
func feeBps(rate string) int64 {
	d, err := model.ParseDecimal(rate)
	if err != nil {
		return 0
	}
	return int64(math.Round(d.Float64() * 10000))
}
