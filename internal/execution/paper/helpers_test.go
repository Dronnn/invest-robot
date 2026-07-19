package paper

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/execution"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// base is a fixed reference instant every test builds its clock and quotes
// around, so freshness and session windows are deterministic.
var base = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "paper.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedInstrument(t *testing.T, db *sqlite.DB, uid string, lot int64, tick string) model.Instrument {
	t.Helper()
	instr := model.Instrument{
		InstrumentRef:     model.InstrumentRef{UID: model.InstrumentUID(uid), FIGI: model.FIGI("F-" + uid), Ticker: "TCK", ClassCode: "TQBR"},
		Lot:               lot,
		MinPriceIncrement: model.MustDecimal(tick),
		Currency:          "rub",
		Name:              "Test " + uid,
	}
	if err := (sqlite.InstrumentRepo{}).Upsert(context.Background(), db, instr, base); err != nil {
		t.Fatalf("seed instrument %s: %v", uid, err)
	}
	return instr
}

// seedDecision inserts a cycle and a decision, returning the decision id used as
// an intent's foreign key. Each call makes a fresh decision row.
func seedDecision(t *testing.T, db *sqlite.DB, uid model.InstrumentUID) int64 {
	t.Helper()
	ctx := context.Background()
	cycleID, err := (sqlite.CycleRepo{}).Insert(ctx, db, sqlite.Cycle{
		StartedAt: base, AsOf: base, Mode: "paper", Engine: "rules",
		EngineVersion: "v1", PromptTemplateHash: "h", ConfigSnapshot: "{}", Status: "running",
	})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	decID, err := (sqlite.DecisionRepo{}).Insert(ctx, db, sqlite.DecisionRecord{
		CycleID:          cycleID,
		Decision:         model.Decision{InstrumentUID: uid, Action: model.ActionBuy, Quantity: 1, OrderType: model.OrderMarket, TimeInForce: model.TIFDay},
		ValidationStatus: "valid",
	})
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	return decID
}

// fakeApplier records every ApplyFill it is handed and returns err (nil unless a
// test wants to force a rollback). It records before returning err so a rollback
// test can still confirm the applier ran inside the transaction.
type fakeApplier struct {
	mu    sync.Mutex
	calls []execution.FillApplication
	err   error
}

func (f *fakeApplier) ApplyFill(ctx context.Context, q sqlite.Querier, fa execution.FillApplication) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fa)
	return f.err
}

func (f *fakeApplier) recorded() []execution.FillApplication {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execution.FillApplication(nil), f.calls...)
}

// newSim builds a Simulator with the given slippage/commission and a one-minute
// freshness window, on a clock fixed at base.
func newSim(t *testing.T, db *sqlite.DB, clk clock.Clock, applier execution.FillApplier, slippageBps int, commission string) *Simulator {
	t.Helper()
	s, err := New(db, clk, applier, config.PaperConfig{StartingCash: "100000", SlippageBps: slippageBps, CommissionRate: commission}, time.Minute)
	if err != nil {
		t.Fatalf("New simulator: %v", err)
	}
	return s
}

// quote builds a top-of-book quote at time base+offset.
func quote(uid model.InstrumentUID, bid, ask, last string, offset time.Duration) model.Quote {
	return model.Quote{
		InstrumentUID: uid,
		Bid:           mustDec(bid),
		Ask:           mustDec(ask),
		Last:          mustDec(last),
		TS:            base.Add(offset),
	}
}

func mustDec(s string) model.Decimal {
	if s == "" {
		return model.Decimal{}
	}
	return model.MustDecimal(s)
}

func decPtr(s string) *model.Decimal {
	d := model.MustDecimal(s)
	return &d
}

// submit journals one decision and returns the client order id of the intent it
// created (the newest intent row).
func submit(t *testing.T, s *Simulator, db *sqlite.DB, d model.Decision, instr model.Instrument, q model.Quote, sess execution.Session) string {
	t.Helper()
	decID := seedDecision(t, db, instr.UID)
	sc := execution.SubmitContext{
		Instruments: map[model.InstrumentUID]execution.InstrumentContext{instr.UID: {Instrument: instr, Quote: q}},
		DecisionIDs: []int64{decID},
		Session:     sess,
	}
	before := make(map[string]struct{})
	for _, r := range loadIntents(t, db) {
		before[r.ID] = struct{}{}
	}
	if err := s.Submit(context.Background(), []model.Decision{d}, sc); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	var created []string
	for _, r := range loadIntents(t, db) {
		if _, seen := before[r.ID]; !seen {
			created = append(created, r.ID)
		}
	}
	if len(created) != 1 {
		t.Fatalf("Submit created %d intents, want 1", len(created))
	}
	return created[0]
}

func buyMarket(uid model.InstrumentUID, qty int64) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionBuy, Quantity: qty, OrderType: model.OrderMarket, TimeInForce: model.TIFDay}
}

func sellMarket(uid model.InstrumentUID, qty int64) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionSell, Quantity: qty, OrderType: model.OrderMarket, TimeInForce: model.TIFDay}
}

func buyLimit(uid model.InstrumentUID, qty int64, limit string) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionBuy, Quantity: qty, OrderType: model.OrderLimit, LimitPrice: decPtr(limit), TimeInForce: model.TIFDay}
}

func sellLimit(uid model.InstrumentUID, qty int64, limit string) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionSell, Quantity: qty, OrderType: model.OrderLimit, LimitPrice: decPtr(limit), TimeInForce: model.TIFDay}
}

// intentRow is the id+state projection tests assert on; a filled/canceled intent
// is terminal and so absent from IntentRepo.NonTerminal, hence the direct read.
type intentRow struct {
	ID    string
	State model.IntentState
}

func loadIntents(t *testing.T, db *sqlite.DB) []intentRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT client_order_id, state FROM order_intents ORDER BY created_at ASC, client_order_id ASC`)
	if err != nil {
		t.Fatalf("load intents: %v", err)
	}
	defer rows.Close()
	var out []intentRow
	for rows.Next() {
		var r intentRow
		var st string
		if err := rows.Scan(&r.ID, &st); err != nil {
			t.Fatalf("scan intent: %v", err)
		}
		r.State = model.IntentState(st)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate intents: %v", err)
	}
	return out
}

func stateOf(t *testing.T, db *sqlite.DB, id string) model.IntentState {
	t.Helper()
	in, err := (sqlite.IntentRepo{}).Get(context.Background(), db, id)
	if err != nil {
		t.Fatalf("Get intent %s: %v", id, err)
	}
	return in.State
}

func fillsOf(t *testing.T, db *sqlite.DB, id string) []model.Fill {
	t.Helper()
	fs, err := (sqlite.FillRepo{}).ListByIntent(context.Background(), db, id)
	if err != nil {
		t.Fatalf("list fills %s: %v", id, err)
	}
	return fs
}
