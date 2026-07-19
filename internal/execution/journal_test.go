package execution

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

func journalDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedDecisionRow creates the instrument, cycle and decision rows an order
// intent's foreign keys require, returning the decision id.
func seedDecisionRow(t *testing.T, db *sqlite.DB, uid model.InstrumentUID) int64 {
	t.Helper()
	ctx := context.Background()
	instr := model.Instrument{
		InstrumentRef:     model.InstrumentRef{UID: uid, FIGI: model.FIGI("F-" + uid), Ticker: "TCK", ClassCode: "TQBR"},
		Lot:               1,
		MinPriceIncrement: model.MustDecimal("0.01"),
		Currency:          "rub",
		Name:              "n",
	}
	if err := (sqlite.InstrumentRepo{}).Upsert(ctx, db, instr, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("seed instrument: %v", err)
	}
	cycleID, err := (sqlite.CycleRepo{}).Insert(ctx, db, sqlite.Cycle{
		StartedAt: time.Unix(0, 0).UTC(), AsOf: time.Unix(0, 0).UTC(),
		Mode: "paper", Engine: "rules", EngineVersion: "v1", PromptTemplateHash: "h", ConfigSnapshot: "{}", Status: "running",
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

func TestJournal_OpenPersistsNewBeforeAnyTransition(t *testing.T) {
	ctx := context.Background()
	db := journalDB(t)
	decID := seedDecisionRow(t, db, "uid-1")
	j := NewJournal(clock.NewSimulated(time.Unix(1000, 0)))

	in, err := j.Open(ctx, db, NewIntent{
		DecisionID: decID, InstrumentUID: "uid-1", Side: model.SideBuy,
		Qty: 2, Type: model.OrderMarket, TimeInForce: model.TIFDay,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if in.ClientOrderID == "" {
		t.Fatal("Open returned an empty client order id")
	}

	// The durable row must exist in state `new` before any submission or fill.
	got, err := (sqlite.IntentRepo{}).Get(ctx, db, in.ClientOrderID)
	if err != nil {
		t.Fatalf("Get after Open: %v", err)
	}
	if got.State != model.IntentNew {
		t.Fatalf("state after Open = %s, want new", got.State)
	}
	if !got.CreatedAt.Equal(time.Unix(1000, 0).UTC()) {
		t.Errorf("CreatedAt = %s, want the clock time", got.CreatedAt)
	}

	// Only then do the disciplined transitions apply.
	if err := j.Transition(ctx, db, in.ClientOrderID, model.IntentNew, model.IntentSubmitted); err != nil {
		t.Fatalf("Transition new->submitted: %v", err)
	}
	if err := j.Transition(ctx, db, in.ClientOrderID, model.IntentSubmitted, model.IntentAcked); err != nil {
		t.Fatalf("Transition submitted->acked: %v", err)
	}
	got, _ = (sqlite.IntentRepo{}).Get(ctx, db, in.ClientOrderID)
	if got.State != model.IntentAcked {
		t.Errorf("final state = %s, want acked", got.State)
	}
}

func TestJournal_TransitionWithReasonPersistsReason(t *testing.T) {
	ctx := context.Background()
	db := journalDB(t)
	decID := seedDecisionRow(t, db, "uid-1")
	j := NewJournal(clock.NewSimulated(time.Unix(1000, 0)))

	in, err := j.Open(ctx, db, NewIntent{DecisionID: decID, InstrumentUID: "uid-1", Side: model.SideBuy, Qty: 1, Type: model.OrderMarket, TimeInForce: model.TIFDay})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := j.TransitionWithReason(ctx, db, in.ClientOrderID, model.IntentNew, model.IntentRejected, "unknown or invalid price tick"); err != nil {
		t.Fatalf("TransitionWithReason: %v", err)
	}
	got, err := (sqlite.IntentRepo{}).Get(ctx, db, in.ClientOrderID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != model.IntentRejected {
		t.Errorf("state = %s, want rejected", got.State)
	}
	if got.Reason != "unknown or invalid price tick" {
		t.Errorf("reason = %q, want %q", got.Reason, "unknown or invalid price tick")
	}
}

func TestJournal_TransitionSurfacesCASConflict(t *testing.T) {
	ctx := context.Background()
	db := journalDB(t)
	decID := seedDecisionRow(t, db, "uid-1")
	j := NewJournal(clock.NewSimulated(time.Unix(1000, 0)))

	in, err := j.Open(ctx, db, NewIntent{DecisionID: decID, InstrumentUID: "uid-1", Side: model.SideBuy, Qty: 1, Type: model.OrderMarket, TimeInForce: model.TIFDay})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := j.Transition(ctx, db, in.ClientOrderID, model.IntentNew, model.IntentSubmitted); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	// A second writer still expecting `new` must lose the compare-and-swap.
	err = j.Transition(ctx, db, in.ClientOrderID, model.IntentNew, model.IntentSubmitted)
	var conflict sqlite.StateConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale transition error = %v, want StateConflictError", err)
	}
	if conflict.Expected != model.IntentNew || conflict.Actual != model.IntentSubmitted {
		t.Errorf("conflict = %+v, want expected=new actual=submitted", conflict)
	}
}
