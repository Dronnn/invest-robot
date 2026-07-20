package cycle_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/cycle"
	"github.com/Dronnn/invest-robot/internal/decision"
	"github.com/Dronnn/invest-robot/internal/decision/rules"
	"github.com/Dronnn/invest-robot/internal/execution"
	"github.com/Dronnn/invest-robot/internal/execution/paper"
	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/market"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/portfolio"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
)

const uidSBER = "e6123145-9665-43e0-8413-cd61b8aa9b13"

var baseTime = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

// candleFixtureJSON is a four-bar 5m series (closes 100,102,101,103) crafted so
// the rules engine sees a bullish EMA with RSI in range and a positive ATR — a
// buy — with tiny indicator periods.
const candleFixtureJSON = `{"instrument_uid":"` + uidSBER + `","interval":"5m","candles":[` +
	`{"time":"2026-07-20T11:40:00Z","open":{"value":"100"},"high":{"value":"101"},"low":{"value":"99"},"close":{"value":"100"},"volume":"100","is_complete":true},` +
	`{"time":"2026-07-20T11:45:00Z","open":{"value":"100"},"high":{"value":"103"},"low":{"value":"101"},"close":{"value":"102"},"volume":"100","is_complete":true},` +
	`{"time":"2026-07-20T11:50:00Z","open":{"value":"102"},"high":{"value":"102"},"low":{"value":"100"},"close":{"value":"101"},"volume":"100","is_complete":true},` +
	`{"time":"2026-07-20T11:55:00Z","open":{"value":"101"},"high":{"value":"104"},"low":{"value":"102"},"close":{"value":"103"},"volume":"100","is_complete":true}` +
	`]}`

// --- fake broker + stream handle for the collector ---

type fakeHandle struct {
	ch   chan tinvestcli.Event
	once sync.Once
}

func (h *fakeHandle) Events() <-chan tinvestcli.Event { return h.ch }
func (h *fakeHandle) Close() error                    { h.once.Do(func() { close(h.ch) }); return nil }

type fakeBroker struct{ handles chan *fakeHandle }

func (b *fakeBroker) InstrumentGet(_ context.Context, _ string) (tinvestcli.Instrument, error) {
	return tinvestcli.Instrument{
		UID: uidSBER, Ticker: "SBER", ClassCode: "TQBR", FIGI: "BBG004730N88",
		Lot: 1, Currency: "rub", MinPriceIncrement: tinvestcli.Money{Amount: model.MustDecimal("0.01")},
	}, nil
}

func (b *fakeBroker) CandlesGet(_ context.Context, _ string, _ model.CandleInterval, _, _ time.Time) (tinvestcli.CandlesResult, error) {
	var res tinvestcli.CandlesResult
	_ = json.Unmarshal([]byte(candleFixtureJSON), &res)
	return res, nil
}

func (b *fakeBroker) StreamMarketdata(_ context.Context, _ tinvestcli.StreamRequest) (market.StreamHandle, error) {
	h := &fakeHandle{ch: make(chan tinvestcli.Event, 8)}
	b.handles <- h
	return h, nil
}

// --- portfolio applier adapter (execution.FillApplier) ---

type applier struct{ pf *portfolio.Portfolio }

func (a applier) ApplyFill(ctx context.Context, q sqlite.Querier, fa execution.FillApplication) error {
	return a.pf.ApplyFill(ctx, q, portfolio.FillApplication{
		Fill: fa.Fill, InstrumentUID: fa.InstrumentUID, Side: fa.Side, Lot: fa.Lot,
		Currency: fa.Currency, LowFidelity: fa.LowFidelity,
	})
}

// --- rig ---

type rig struct {
	db        *sqlite.DB
	clk       *clock.Simulated
	eng       *cycle.Engine
	sim       *paper.Simulator
	collector *market.Collector
}

func testRig(t *testing.T, clk *clock.Simulated) *rig {
	t.Helper()
	db, err := sqlite.Open(context.Background(), t.TempDir()+"/robot.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	pf := portfolio.New(clk, "rub")
	if err := pf.Init(context.Background(), db, model.MustDecimal("100000")); err != nil {
		t.Fatalf("init portfolio: %v", err)
	}

	sim, err := paper.New(db, clk, applier{pf: pf}, config.PaperConfig{StartingCash: "100000", CommissionRate: "0"}, time.Hour, model.Decimal{}, "rub")
	if err != nil {
		t.Fatalf("paper: %v", err)
	}
	if err := sim.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	store := market.NewSQLiteStore(db)
	broker := &fakeBroker{handles: make(chan *fakeHandle, 4)}
	collector, err := market.New(market.Deps{
		Broker: broker, Instruments: store, Candles: store, Quotes: store, Events: store, Clock: clk,
	}, market.Config{Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m})
	if err != nil {
		t.Fatalf("collector: %v", err)
	}

	eng, err := cycle.New(cycle.Deps{DB: db, Clock: clk, Engine: mustRules(t), Executor: sim, Portfolio: pf}, cycle.Config{
		Mode:          "paper",
		Interval:      model.Interval5m,
		Currency:      "rub",
		FeatureParams: features.Params{SMAPeriod: 2, EMAFastPeriod: 2, EMASlowPeriod: 3, RSIPeriod: 2, ATRPeriod: 2},
		Risk:          generousRisk(),
		Paper:         config.PaperConfig{StartingCash: "100000", CommissionRate: "0"},
		MaxDataAge:    time.Hour,
	})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	return &rig{db: db, clk: clk, eng: eng, sim: sim, collector: collector}
}

func mustRules(t *testing.T) decision.Engine {
	t.Helper()
	e, err := rules.New(rules.Params{
		ATRMultiplier: 2, RiskFractionBps: 1000, RSIEntryLow: 1, RSIEntryHigh: 99, RSIExitHigh: 100,
		MaxDataAge: time.Hour, ConfidenceBase: 0.6, ConfidenceRSIBonusMax: 0.3,
	}, clock.NewSimulated(baseTime))
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	return e
}

func generousRisk() config.RiskConfig {
	return config.RiskConfig{
		MaxPositionNotional: "1000000", MaxTotalExposure: "1000000",
		MaxOrdersPerCycle: 5, MaxOrdersPerDay: 20, MaxDailyLoss: "1000000", CashFloor: "0",
	}
}

// backfillCandles starts the collector so it backfills the fixture candles and
// persists the instrument, then waits for them to land.
func (r *rig) backfillCandles(t *testing.T) {
	t.Helper()
	if err := r.collector.Start(context.Background()); err != nil {
		t.Fatalf("collector start: %v", err)
	}
	t.Cleanup(r.collector.Stop)
	waitFor(t, "candles collected", func() bool {
		got, err := sqlite.CandleRepo{}.Range(context.Background(), r.db, uidSBER, model.Interval5m,
			baseTime.Add(-time.Hour), baseTime)
		return err == nil && len(got) == 4
	})
}

func (r *rig) seedQuote(t *testing.T, last string, ts time.Time) {
	t.Helper()
	if err := (sqlite.QuoteRepo{}).Insert(context.Background(), r.db, model.Quote{
		InstrumentUID: uidSBER, Last: model.MustDecimal(last), TS: ts,
	}); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- tests ---

func TestFullLoop(t *testing.T) {
	clk := clock.NewSimulated(baseTime)
	r := testRig(t, clk)
	r.backfillCandles(t)
	r.seedQuote(t, "103", baseTime.Add(-time.Minute)) // as-of quote for risk pricing
	ctx := context.Background()

	// Cycle 1 at baseTime: rules buys, risk passes, an intent is journaled.
	sum, err := r.eng.RunOnce(ctx)
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if sum.Status != "ok" || sum.Orders != 1 {
		t.Fatalf("cycle 1 summary = %+v, want ok with 1 order", sum)
	}

	// Feature snapshot was built and persisted.
	if _, ok, _ := (sqlite.FeatureSnapshotRepo{}).Latest(ctx, r.db, uidSBER); !ok {
		t.Fatal("no feature snapshot persisted")
	}
	// A buy decision was persisted and allowed.
	decs, _ := (sqlite.DecisionRepo{}).ListByCycle(ctx, r.db, sum.ID)
	if len(decs) != 1 || decs[0].Decision.Action != model.ActionBuy || decs[0].ValidationStatus != "allowed" {
		t.Fatalf("decision rows = %+v", decs)
	}
	// The engine call was recorded for replay.
	calls, _ := (sqlite.LLMCallRepo{}).ListByCycle(ctx, r.db, sum.ID)
	if len(calls) != 1 || len(calls[0].Request) == 0 {
		t.Fatalf("llm_calls = %+v", calls)
	}
	// An intent was journaled with a UUID client order id, resting acked.
	intents, _ := (sqlite.IntentRepo{}).NonTerminal(ctx, r.db)
	if len(intents) != 1 || intents[0].State != model.IntentAcked || len(intents[0].ClientOrderID) != 36 {
		t.Fatalf("intents = %+v", intents)
	}
	boughtLots := intents[0].Qty

	// Next observation: a later quote fills the resting order.
	clk.Advance(time.Minute)
	if err := r.sim.OnQuote(ctx, model.Quote{InstrumentUID: uidSBER, Last: model.MustDecimal("103"), TS: clk.Now()}); err != nil {
		t.Fatalf("on quote: %v", err)
	}

	pos, ok, _ := (sqlite.PositionRepo{}).Get(ctx, r.db, uidSBER)
	if !ok || pos.Qty != boughtLots {
		t.Fatalf("position = %+v, want %d lots", pos, boughtLots)
	}
	cash, _ := (sqlite.CashRepo{}).Balance(ctx, r.db, "rub")
	if cash.Cmp(model.MustDecimal("100000")) >= 0 {
		t.Fatalf("cash %s not reduced by the fill", cash)
	}
	fills, _ := (sqlite.FillRepo{}).Recent(ctx, r.db, -1)
	if len(fills) != 1 {
		t.Fatalf("fills = %d, want 1", len(fills))
	}
	if snap, ok, _ := (sqlite.EquityRepo{}).Latest(ctx, r.db); !ok || snap.Total.IsZero() {
		t.Fatalf("no equity snapshot")
	}

	// Cycle 2 runs (position now open → hold/exit), proving ≥2 cycles.
	clk.Advance(5 * time.Minute)
	if _, err := r.eng.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	cycles, _ := (sqlite.CycleRepo{}).Recent(ctx, r.db, 10)
	if len(cycles) < 2 {
		t.Fatalf("cycles = %d, want >= 2", len(cycles))
	}
}

func TestDeterministicDecisions(t *testing.T) {
	run := func() string {
		clk := clock.NewSimulated(baseTime)
		r := testRig(t, clk)
		r.backfillCandles(t)
		r.seedQuote(t, "103", baseTime.Add(-time.Minute))
		sum, err := r.eng.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		calls, _ := (sqlite.LLMCallRepo{}).ListByCycle(context.Background(), r.db, sum.ID)
		if len(calls) != 1 {
			t.Fatalf("want 1 llm call, got %d", len(calls))
		}
		return calls[0].Response // the engine's raw marshaled decisions
	}
	a, b := run(), run()
	if a != b || a == "" {
		t.Fatalf("decisions not byte-identical across runs:\n a=%s\n b=%s", a, b)
	}
}

func TestStaleDataSkipsCycle(t *testing.T) {
	clk := clock.NewSimulated(baseTime)
	r := testRig(t, clk)
	r.backfillCandles(t)
	r.seedQuote(t, "103", baseTime.Add(-time.Minute))
	// Advance far past MaxDataAge so every instrument's data is stale.
	clk.Advance(2 * time.Hour)
	sum, err := r.eng.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.Status != "skipped" {
		t.Fatalf("cycle status = %q, want skipped on stale data", sum.Status)
	}
	if intents, _ := (sqlite.IntentRepo{}).NonTerminal(context.Background(), r.db); len(intents) != 0 {
		t.Fatalf("stale cycle should place no orders, got %d intents", len(intents))
	}
}

func TestKillSwitchFlattenOnly(t *testing.T) {
	clk := clock.NewSimulated(baseTime)
	r := testRig(t, clk)
	r.backfillCandles(t)
	r.seedQuote(t, "103", baseTime.Add(-time.Minute))

	r.eng.KillSwitch() // latch the durable halt
	if r.eng.Status().State != cycle.StateHalted {
		t.Fatalf("state = %q, want halted after kill switch", r.eng.Status().State)
	}

	sum, err := r.eng.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.Orders != 0 {
		t.Fatalf("halted cycle placed %d orders, want 0 (flatten-only)", sum.Orders)
	}
	// The buy was stripped by risk and recorded on its decision row.
	decs, _ := (sqlite.DecisionRepo{}).ListByCycle(context.Background(), r.db, sum.ID)
	if len(decs) != 1 || decs[0].Decision.Action != model.ActionBuy {
		t.Fatalf("decision rows = %+v", decs)
	}
	if decs[0].ValidationStatus == "allowed" {
		t.Fatalf("halted buy should be stripped, got status %q", decs[0].ValidationStatus)
	}
}

func TestCrashRecoveryCancelsStrandedSubmit(t *testing.T) {
	clk := clock.NewSimulated(baseTime)
	r := testRig(t, clk)
	ctx := context.Background()

	// The instrument must exist for the intent's foreign key.
	if err := (sqlite.InstrumentRepo{}).Upsert(ctx, r.db, model.Instrument{
		InstrumentRef:     model.InstrumentRef{UID: uidSBER, Ticker: "SBER", ClassCode: "TQBR"},
		Lot:               1,
		MinPriceIncrement: model.MustDecimal("0.01"),
		Currency:          "rub",
	}, clk.Now()); err != nil {
		t.Fatalf("seed instrument: %v", err)
	}

	// Seed a stranded submitted intent (a crash mid-submission).
	stranded := model.OrderIntent{
		ClientOrderID: "11111111-1111-1111-1111-111111111111", DecisionID: 1,
		InstrumentUID: uidSBER, Side: model.SideBuy, Qty: 1, Type: model.OrderMarket,
		TimeInForce: model.TIFDay, State: model.IntentNew, CreatedAt: clk.Now(), UpdatedAt: clk.Now(),
	}
	// A decision row is needed for the NOT NULL foreign key.
	cid, _ := (sqlite.CycleRepo{}).Insert(ctx, r.db, sqlite.Cycle{StartedAt: clk.Now(), AsOf: clk.Now(), Mode: "paper", Engine: "rules", Status: "ok"})
	did, _ := (sqlite.DecisionRepo{}).Insert(ctx, r.db, sqlite.DecisionRecord{CycleID: cid, Decision: model.Decision{InstrumentUID: uidSBER, Action: model.ActionBuy, Quantity: 1, OrderType: model.OrderMarket, TimeInForce: model.TIFDay}, ValidationStatus: "allowed"})
	stranded.DecisionID = did
	if err := (sqlite.IntentRepo{}).Insert(ctx, r.db, stranded); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := (sqlite.IntentRepo{}).UpdateState(ctx, r.db, stranded.ClientOrderID, model.IntentNew, model.IntentSubmitted, clk.Now()); err != nil {
		t.Fatalf("advance to submitted: %v", err)
	}

	// Startup reconciliation cancels the submitted-but-never-acked intent.
	if err := r.sim.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	got, err := (sqlite.IntentRepo{}).Get(ctx, r.db, stranded.ClientOrderID)
	if err != nil {
		t.Fatalf("get intent: %v", err)
	}
	if got.State != model.IntentCanceled {
		t.Fatalf("stranded intent state = %q, want canceled", got.State)
	}
	if got.Reason == "" {
		t.Fatal("canceled intent should carry a reason")
	}
}
