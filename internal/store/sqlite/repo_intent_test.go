package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestIntentRepo_InsertGetUpdateState(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	repo := IntentRepo{}

	in := seedIntent(t, db, decisionID, i.UID, "client-order-1")

	got, err := repo.Get(ctx, db, "client-order-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClientOrderID != in.ClientOrderID || got.State != model.IntentNew || got.Side != model.SideBuy {
		t.Errorf("Get() = %+v, want %+v", got, in)
	}

	if err := repo.UpdateState(ctx, db, "client-order-1", model.IntentSubmitted, nowUTC()); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	got, err = repo.Get(ctx, db, "client-order-1")
	if err != nil {
		t.Fatalf("Get after UpdateState: %v", err)
	}
	if got.State != model.IntentSubmitted {
		t.Errorf("State after UpdateState = %s, want submitted", got.State)
	}
}

func TestIntentRepo_GetNotFound(t *testing.T) {
	db := openTest(t)
	_, err := (IntentRepo{}).Get(context.Background(), db, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func TestIntentRepo_UpdateStateNotFound(t *testing.T) {
	db := openTest(t)
	err := (IntentRepo{}).UpdateState(context.Background(), db, "missing", model.IntentAcked, nowUTC())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateState(missing) error = %v, want ErrNotFound", err)
	}
}

func TestIntentRepo_NonTerminal(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	repo := IntentRepo{}

	seedIntent(t, db, decisionID, i.UID, "co-new")     // stays new: non-terminal
	seedIntent(t, db, decisionID, i.UID, "co-filled")  // will become filled: terminal
	seedIntent(t, db, decisionID, i.UID, "co-unknown") // becomes unknown: non-terminal

	if err := repo.UpdateState(ctx, db, "co-filled", model.IntentSubmitted, nowUTC()); err != nil {
		t.Fatalf("UpdateState submitted: %v", err)
	}
	if err := repo.UpdateState(ctx, db, "co-filled", model.IntentFilled, nowUTC()); err != nil {
		t.Fatalf("UpdateState filled: %v", err)
	}
	if err := repo.UpdateState(ctx, db, "co-unknown", model.IntentUnknown, nowUTC()); err != nil {
		t.Fatalf("UpdateState unknown: %v", err)
	}

	list, err := repo.NonTerminal(ctx, db)
	if err != nil {
		t.Fatalf("NonTerminal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(NonTerminal) = %d, want 2", len(list))
	}
	ids := map[string]bool{}
	for _, in := range list {
		ids[in.ClientOrderID] = true
		if in.State.IsTerminal() {
			t.Errorf("NonTerminal returned a terminal intent %s in state %s", in.ClientOrderID, in.State)
		}
	}
	if !ids["co-new"] || !ids["co-unknown"] {
		t.Errorf("NonTerminal = %v, want co-new and co-unknown", ids)
	}
	if ids["co-filled"] {
		t.Error("NonTerminal included the filled (terminal) intent")
	}
}

func TestIntentRepo_ClientOrderIDIsUnique(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)

	seedIntent(t, db, decisionID, i.UID, "dup")
	dup := model.OrderIntent{
		ClientOrderID: "dup",
		DecisionID:    decisionID,
		InstrumentUID: i.UID,
		Side:          model.SideSell,
		Qty:           1,
		Type:          model.OrderMarket,
		TimeInForce:   model.TIFDay,
		State:         model.IntentNew,
		CreatedAt:     nowUTC(),
		UpdatedAt:     nowUTC(),
	}
	if err := (IntentRepo{}).Insert(ctx, db, dup); err == nil {
		t.Fatal("expected a primary-key violation inserting a duplicate client_order_id, got nil error")
	}
}
