package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/portfolio"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

const testCurrency = "rub"

// testSessionStart is a fixed baseline so DayPnL is deterministic.
var testSessionStart = time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)

func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// openTestDB opens a migrated SQLite database on a throwaway file.
func openTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "tui-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestReadModel builds a readModel over db with a real-clock portfolio.
func newTestReadModel(db *sqlite.DB) *readModel {
	return &readModel{
		q:            db,
		pf:           portfolio.New(clock.Real(), testCurrency),
		clk:          clock.Real(),
		currency:     testCurrency,
		sessionStart: testSessionStart,
		baseCtx:      context.Background(),
		queryTimeout: 2 * time.Second,
	}
}

// seededIDs carries identifiers produced by seedFullScenario.
type seededIDs struct {
	uid           model.InstrumentUID
	ticker        string
	cycleID       int64
	decisionID    int64
	clientOrderID string
}

// seedFullScenario writes one of (almost) everything the screens read: an
// instrument, a held+priced position, cash, two equity snapshots, a cycle with
// a decision and an engine call, a non-terminal order intent, a fill, and a few
// events across levels.
func seedFullScenario(t *testing.T, db *sqlite.DB) seededIDs {
	t.Helper()
	ctx := context.Background()
	now := nowUTC()

	uid := model.InstrumentUID("uid-sber")
	inst := model.Instrument{
		InstrumentRef:     model.InstrumentRef{UID: uid, FIGI: "FIGI-SBER", Ticker: "SBER", ClassCode: "TQBR"},
		Lot:               10,
		MinPriceIncrement: model.MustDecimal("0.01"),
		Currency:          testCurrency,
		Name:              "Sberbank",
	}
	if err := (sqlite.InstrumentRepo{}).Upsert(ctx, db, inst, now); err != nil {
		t.Fatalf("seed instrument: %v", err)
	}

	if err := (sqlite.PositionRepo{}).Upsert(ctx, db, model.Position{
		InstrumentUID: uid, Qty: 2, AvgPrice: model.MustDecimal("100"), UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if err := (sqlite.QuoteRepo{}).Insert(ctx, db, model.Quote{
		InstrumentUID: uid, Bid: model.MustDecimal("149.9"), Ask: model.MustDecimal("150.1"),
		Last: model.MustDecimal("150"), TS: now,
	}); err != nil {
		t.Fatalf("seed quote: %v", err)
	}

	// Cash: 100000 rub deposit.
	if _, err := (sqlite.CashRepo{}).Insert(ctx, db, sqlite.CashEntry{
		TS: testSessionStart, Delta: model.MustDecimal("100000"), Currency: testCurrency, Reason: "deposit",
	}); err != nil {
		t.Fatalf("seed cash: %v", err)
	}

	// Equity curve: session baseline 100000, latest 105000 -> day total +5000.
	if _, err := (sqlite.EquityRepo{}).Insert(ctx, db, sqlite.EquitySnapshot{
		TS: testSessionStart, Cash: model.MustDecimal("100000"), MarketValue: model.Decimal{}, Total: model.MustDecimal("100000"),
	}); err != nil {
		t.Fatalf("seed equity baseline: %v", err)
	}
	if _, err := (sqlite.EquityRepo{}).Insert(ctx, db, sqlite.EquitySnapshot{
		TS: testSessionStart.Add(time.Hour), Cash: model.MustDecimal("97000"), MarketValue: model.MustDecimal("8000"), Total: model.MustDecimal("105000"),
	}); err != nil {
		t.Fatalf("seed equity latest: %v", err)
	}

	cycleID, err := (sqlite.CycleRepo{}).Insert(ctx, db, sqlite.Cycle{
		StartedAt: now, AsOf: now, Mode: "paper", Engine: "rules", EngineVersion: "v1",
		PromptTemplateHash: "hash", ConfigSnapshot: "{}", Status: "ok",
	})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	decisionID, err := (sqlite.DecisionRepo{}).Insert(ctx, db, sqlite.DecisionRecord{
		CycleID: cycleID,
		Decision: model.Decision{
			InstrumentUID: uid, Action: model.ActionBuy, Quantity: 1, OrderType: model.OrderMarket,
			TimeInForce: model.TIFDay, Rationale: "momentum breakout above SMA20", Confidence: 0.75,
		},
		ValidationStatus: "valid",
	})
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}

	if _, err := (sqlite.LLMCallRepo{}).Insert(ctx, db, sqlite.LLMCall{
		CycleID: cycleID, Model: "rules", Request: `{"as_of":"..."}`, Response: `{"actions":[]}`,
		DurationMS: 12, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed llm_call: %v", err)
	}

	clientOrderID := "order-0001"
	if err := (sqlite.IntentRepo{}).Insert(ctx, db, model.OrderIntent{
		ClientOrderID: clientOrderID, DecisionID: decisionID, InstrumentUID: uid, Side: model.SideBuy,
		Qty: 1, Type: model.OrderMarket, TimeInForce: model.TIFDay, State: model.IntentNew,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := (sqlite.FillRepo{}).Insert(ctx, db, model.Fill{
		IntentID: clientOrderID, Price: model.MustDecimal("150"), Qty: 1, Fee: model.MustDecimal("0.75"), TS: now,
	}, false); err != nil {
		t.Fatalf("seed fill: %v", err)
	}

	for _, e := range []sqlite.Event{
		{TS: now.Add(-3 * time.Second), Level: "info", Code: "cycle_started", Payload: `{"id":1}`},
		{TS: now.Add(-2 * time.Second), Level: "warn", Code: "quote_stale", Payload: ""},
		{TS: now.Add(-1 * time.Second), Level: "error", Code: "order_rejected", Payload: `{"reason":"tick"}`},
	} {
		if _, err := (sqlite.EventRepo{}).Insert(ctx, db, e); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}

	return seededIDs{uid: uid, ticker: "SBER", cycleID: cycleID, decisionID: decisionID, clientOrderID: clientOrderID}
}

// newTestApp builds a fully-wired App over a seeded (or empty) db, returning the
// app plus the stubs so tests can assert on controller/cancel routing.
func newTestApp(t *testing.T, db *sqlite.DB) (*App, *StubController, *StubCancelRequester) {
	t.Helper()
	ctrl := NewStubController("PAPER")
	canceller := &StubCancelRequester{}
	app, err := New(Deps{
		DB:           db,
		Portfolio:    portfolio.New(clock.Real(), testCurrency),
		Controller:   ctrl,
		Canceller:    canceller,
		Clock:        clock.Real(),
		Currency:     testCurrency,
		Mode:         "PAPER",
		SessionStart: testSessionStart,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	app.rm.baseCtx = context.Background()
	return app, ctrl, canceller
}
