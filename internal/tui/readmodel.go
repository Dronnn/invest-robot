package tui

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/market"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/portfolio"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// readModel fetches and shapes the data every screen renders. It is the only
// component that touches the store / portfolio / market packages, and it always
// runs inside a tea.Cmd (i.e. off the UI goroutine) with a context-bounded
// query. Nothing here mutates state — the TUI is a read-only client of app
// state (DESIGN §3).
type readModel struct {
	q            sqlite.Querier
	pf           *portfolio.Portfolio
	health       HealthProvider
	clk          clock.Clock
	currency     string
	sessionStart time.Time

	// baseCtx is the parent context the command closures derive their bounded
	// query contexts from; App.Run sets it from the context passed to Run.
	// Storing it here (rather than threading it through every screen) keeps the
	// screen commands parameterless; it is only read, never cancelled, here.
	baseCtx      context.Context
	queryTimeout time.Duration
}

// listLimit caps how many rows the list screens pull per refresh — enough to
// fill any terminal, small enough to keep each query cheap.
const listLimit = 200

// queryCtx derives a bounded context for one fetch from baseCtx.
func (rm *readModel) queryCtx() (context.Context, context.CancelFunc) {
	parent := rm.baseCtx
	if parent == nil {
		parent = context.Background()
	}
	timeout := rm.queryTimeout
	if timeout <= 0 {
		timeout = defaultQueryTimeout
	}
	return context.WithTimeout(parent, timeout)
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

// dashboardData is the shaped Dashboard payload.
type dashboardData struct {
	cash          model.Decimal
	equity        model.Decimal
	equityKnown   bool
	dayTotal      model.Decimal
	dayRealized   model.Decimal
	dayUnrealized model.Decimal
	dayPnLKnown   bool
	positionCount int
	health        market.Health
	healthKnown   bool
	warn          string // soft, non-fatal note (e.g. degraded pricing)
	err           error  // hard fetch error to surface
}

func (rm *readModel) dashboard(ctx context.Context) dashboardData {
	var d dashboardData

	cash, err := (sqlite.CashRepo{}).Balance(ctx, rm.q, rm.currency)
	if err != nil {
		d.err = err
		return d
	}
	d.cash = cash

	positions, err := (sqlite.PositionRepo{}).List(ctx, rm.q)
	if err != nil {
		d.err = err
		return d
	}
	held := heldPositions(positions)
	d.positionCount = len(held)

	quotes := rm.latestQuotes(ctx, held)
	summary, err := rm.pf.Summary(ctx, rm.q, quotes)
	switch {
	case err == nil:
		d.equity = summary.Equity
		d.equityKnown = true
		d.positionCount = len(summary.Positions)
	case isMissingQuote(err):
		d.warn = "equity unpriced: " + err.Error()
	default:
		d.err = err
		return d
	}

	pnl, err := rm.pf.DayPnL(ctx, rm.q, rm.sessionStart)
	switch {
	case err == nil:
		d.dayTotal, d.dayRealized, d.dayUnrealized = pnl.Total, pnl.Realized, pnl.Unrealized
		d.dayPnLKnown = true
	case errors.Is(err, portfolio.ErrSessionSnapshotMissing):
		// No baseline yet — leave day P&L unknown, not an error.
	default:
		if d.warn == "" {
			d.warn = "day P&L unavailable: " + err.Error()
		}
	}

	if rm.health != nil {
		d.health = rm.health.Health()
		d.healthKnown = true
	}
	return d
}

// ---------------------------------------------------------------------------
// Positions
// ---------------------------------------------------------------------------

// positionRow is one row of the Positions table.
type positionRow struct {
	uid       model.InstrumentUID
	ticker    string
	qty       int64
	avgPrice  model.Decimal
	lastPrice model.Decimal
	priced    bool
	pnl       model.Decimal
}

type positionsData struct {
	rows []positionRow
	warn string
	err  error
}

func (rm *readModel) positions(ctx context.Context) positionsData {
	var d positionsData

	positions, err := (sqlite.PositionRepo{}).List(ctx, rm.q)
	if err != nil {
		d.err = err
		return d
	}
	held := heldPositions(positions)
	tickers := rm.tickerIndex(ctx)
	quotes := rm.latestQuotes(ctx, held)

	summary, err := rm.pf.Summary(ctx, rm.q, quotes)
	if err == nil {
		for _, v := range summary.Positions {
			d.rows = append(d.rows, positionRow{
				uid: v.UID, ticker: tickers[v.UID], qty: v.Qty,
				avgPrice: v.AvgPrice, lastPrice: v.LastPrice, priced: true, pnl: v.UnrealizedPnL,
			})
		}
		return d
	}
	if !isMissingQuote(err) {
		d.err = err
		return d
	}

	// Degraded: at least one position could not be priced. Show every held
	// position, pricing the ones we do have a usable quote for and leaving the
	// rest marked unpriced rather than failing the whole screen.
	d.warn = "some positions unpriced (stale/missing quote)"
	for _, p := range held {
		row := positionRow{uid: p.InstrumentUID, ticker: tickers[p.InstrumentUID], qty: p.Qty, avgPrice: p.AvgPrice}
		if q, ok := quotes[p.InstrumentUID]; ok && !q.Last.IsZero() {
			row.lastPrice = q.Last
			row.priced = true
		}
		d.rows = append(d.rows, row)
	}
	return d
}

// fillRow is one execution in a position's fills history.
type fillRow struct {
	intentID string
	price    model.Decimal
	qty      int64
	fee      model.Decimal
	ts       time.Time
}

// fills returns every fill recorded against the given instrument, oldest first,
// by walking the instrument's order intents and their fills (DESIGN §9: "join
// through intents by instrument"). IntentRepo exposes no list-by-instrument
// query, so the client order ids are read with one narrow read-only projection
// and the fill rows themselves come from FillRepo per intent.
func (rm *readModel) fills(ctx context.Context, uid model.InstrumentUID) ([]fillRow, error) {
	ids, err := rm.intentIDsByInstrument(ctx, uid)
	if err != nil {
		return nil, err
	}
	var out []fillRow
	for _, id := range ids {
		fs, err := (sqlite.FillRepo{}).ListByIntent(ctx, rm.q, id)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			out = append(out, fillRow{intentID: f.IntentID, price: f.Price, qty: f.Qty, fee: f.Fee, ts: f.TS})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ts.Before(out[j].ts) })
	return out, nil
}

// intentIDsByInstrument returns the client order ids of every intent for uid,
// oldest first. This is the one raw projection the TUI issues: IntentRepo has
// no by-instrument list method and this package is scoped out of store/sqlite,
// so a single narrow read-only query (one indexed column, no domain decoding)
// bridges the gap without duplicating repository logic.
func (rm *readModel) intentIDsByInstrument(ctx context.Context, uid model.InstrumentUID) ([]string, error) {
	rows, err := rm.q.QueryContext(ctx,
		`SELECT client_order_id FROM order_intents WHERE instrument_uid = ? ORDER BY created_at ASC, client_order_id ASC`,
		string(uid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Decisions (cycle replay)
// ---------------------------------------------------------------------------

type cycleRow struct {
	id        int64
	startedAt time.Time
	asOf      time.Time
	mode      string
	engine    string
	status    string
}

type decisionsData struct {
	rows []cycleRow
	err  error
}

func (rm *readModel) cycles(ctx context.Context) decisionsData {
	var d decisionsData
	cs, err := (sqlite.CycleRepo{}).Recent(ctx, rm.q, listLimit)
	if err != nil {
		d.err = err
		return d
	}
	for _, c := range cs {
		d.rows = append(d.rows, cycleRow{
			id: c.ID, startedAt: c.StartedAt, asOf: c.AsOf, mode: c.Mode, engine: c.Engine, status: c.Status,
		})
	}
	return d
}

type decisionRow struct {
	uid              model.InstrumentUID
	ticker           string
	action           model.Action
	qty              int64
	orderType        model.OrderType
	limitPrice       *model.Decimal
	timeInForce      model.TimeInForce
	confidence       float64
	rationale        string
	validationStatus string
}

type llmCallRow struct {
	model      string
	durationMS int64
	request    string
	response   string
	errMsg     string
	createdAt  time.Time
}

// cycleDetail is the replay payload for one cycle: its decisions (with
// validation status and rationale) and any raw engine calls.
type cycleDetail struct {
	cycleID   int64
	decisions []decisionRow
	llmCalls  []llmCallRow
	err       error
}

func (rm *readModel) cycleDetail(ctx context.Context, cycleID int64) cycleDetail {
	d := cycleDetail{cycleID: cycleID}
	tickers := rm.tickerIndex(ctx)

	decs, err := (sqlite.DecisionRepo{}).ListByCycle(ctx, rm.q, cycleID)
	if err != nil {
		d.err = err
		return d
	}
	for _, r := range decs {
		d.decisions = append(d.decisions, decisionRow{
			uid: r.Decision.InstrumentUID, ticker: tickers[r.Decision.InstrumentUID],
			action: r.Decision.Action, qty: r.Decision.Quantity, orderType: r.Decision.OrderType,
			limitPrice: r.Decision.LimitPrice, timeInForce: r.Decision.TimeInForce,
			confidence: r.Decision.Confidence, rationale: r.Decision.Rationale,
			validationStatus: r.ValidationStatus,
		})
	}

	calls, err := (sqlite.LLMCallRepo{}).ListByCycle(ctx, rm.q, cycleID)
	if err != nil {
		d.err = err
		return d
	}
	for _, c := range calls {
		d.llmCalls = append(d.llmCalls, llmCallRow{
			model: c.Model, durationMS: c.DurationMS, request: c.Request,
			response: c.Response, errMsg: c.Error, createdAt: c.CreatedAt,
		})
	}
	return d
}

// ---------------------------------------------------------------------------
// Orders (open intents)
// ---------------------------------------------------------------------------

type orderRow struct {
	clientOrderID string
	uid           model.InstrumentUID
	ticker        string
	side          model.Side
	qty           int64
	orderType     model.OrderType
	limitPrice    *model.Decimal
	state         model.IntentState
	createdAt     time.Time
	updatedAt     time.Time
}

type ordersData struct {
	rows []orderRow
	err  error
}

// orders returns the non-terminal order intents — the live set the operator can
// still act on (cancel). Terminal intents (filled/canceled/rejected) are not
// cancelable and are surfaced via the position fills history instead.
func (rm *readModel) orders(ctx context.Context) ordersData {
	var d ordersData
	intents, err := (sqlite.IntentRepo{}).NonTerminal(ctx, rm.q)
	if err != nil {
		d.err = err
		return d
	}
	tickers := rm.tickerIndex(ctx)
	for _, in := range intents {
		d.rows = append(d.rows, orderRow{
			clientOrderID: in.ClientOrderID, uid: in.InstrumentUID, ticker: tickers[in.InstrumentUID],
			side: in.Side, qty: in.Qty, orderType: in.Type, limitPrice: in.LimitPrice,
			state: in.State, createdAt: in.CreatedAt, updatedAt: in.UpdatedAt,
		})
	}
	return d
}

// ---------------------------------------------------------------------------
// Log (events)
// ---------------------------------------------------------------------------

type eventRow struct {
	id      int64
	ts      time.Time
	level   string
	code    string
	payload string
}

type logData struct {
	rows []eventRow
	err  error
}

func (rm *readModel) events(ctx context.Context) logData {
	var d logData
	es, err := (sqlite.EventRepo{}).Recent(ctx, rm.q, listLimit)
	if err != nil {
		d.err = err
		return d
	}
	for _, e := range es {
		d.rows = append(d.rows, eventRow{id: e.ID, ts: e.TS, level: e.Level, code: e.Code, payload: e.Payload})
	}
	return d
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// heldPositions filters out zero-qty (fully closed) positions.
func heldPositions(positions []model.Position) []model.Position {
	var out []model.Position
	for _, p := range positions {
		if p.Qty != 0 {
			out = append(out, p)
		}
	}
	return out
}

// latestQuotes builds the quote map portfolio.Summary needs, pulling the latest
// stored quote per held instrument. Instruments without a quote are simply
// absent from the map (Summary then reports them via MissingQuoteError).
func (rm *readModel) latestQuotes(ctx context.Context, held []model.Position) map[model.InstrumentUID]model.Quote {
	quotes := make(map[model.InstrumentUID]model.Quote, len(held))
	for _, p := range held {
		q, ok, err := (sqlite.QuoteRepo{}).Latest(ctx, rm.q, p.InstrumentUID)
		if err != nil || !ok {
			continue
		}
		quotes[p.InstrumentUID] = q
	}
	return quotes
}

// tickerIndex maps instrument uid -> ticker for display. A missing entry is
// fine; callers fall back to the uid.
func (rm *readModel) tickerIndex(ctx context.Context) map[model.InstrumentUID]string {
	index := map[model.InstrumentUID]string{}
	instruments, err := (sqlite.InstrumentRepo{}).List(ctx, rm.q)
	if err != nil {
		return index
	}
	for _, in := range instruments {
		index[in.UID] = in.Ticker
	}
	return index
}

// isMissingQuote reports whether err is portfolio's missing-quote signal.
func isMissingQuote(err error) bool {
	var mq *portfolio.MissingQuoteError
	return errors.As(err, &mq)
}

// displayName returns the ticker if known, else the (possibly shortened) uid.
func displayName(ticker string, uid model.InstrumentUID) string {
	if ticker != "" {
		return ticker
	}
	return string(uid)
}
