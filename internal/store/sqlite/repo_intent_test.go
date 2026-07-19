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

	if err := repo.UpdateState(ctx, db, "client-order-1", model.IntentNew, model.IntentSubmitted, nowUTC()); err != nil {
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
	// A legal transition against a missing intent: the CAS matches no row and
	// the read-back distinguishes "no such intent" from a stale-state conflict.
	err := (IntentRepo{}).UpdateState(context.Background(), db, "missing", model.IntentNew, model.IntentSubmitted, nowUTC())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateState(missing) error = %v, want ErrNotFound", err)
	}
}

func TestIntentRepo_UpdateState_RejectsIllegalTransition(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	repo := IntentRepo{}
	seedIntent(t, db, decisionID, i.UID, "co-1") // starts in state new

	// new -> filled is not an edge of the state machine.
	err := repo.UpdateState(ctx, db, "co-1", model.IntentNew, model.IntentFilled, nowUTC())
	var ill IllegalTransitionError
	if !errors.As(err, &ill) {
		t.Fatalf("UpdateState(new->filled) error = %v, want IllegalTransitionError", err)
	}
	// The stored state must be untouched.
	got, err := repo.Get(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != model.IntentNew {
		t.Errorf("state after rejected transition = %s, want new", got.State)
	}
}

func TestIntentRepo_UpdateState_StaleStateConflict(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	repo := IntentRepo{}
	seedIntent(t, db, decisionID, i.UID, "co-1")

	// Move it forward, then attempt a CAS that still expects the old state.
	if err := repo.UpdateState(ctx, db, "co-1", model.IntentNew, model.IntentSubmitted, nowUTC()); err != nil {
		t.Fatalf("UpdateState(new->submitted): %v", err)
	}
	err := repo.UpdateState(ctx, db, "co-1", model.IntentNew, model.IntentSubmitted, nowUTC())
	var conflict StateConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale UpdateState error = %v, want StateConflictError", err)
	}
	if conflict.Expected != model.IntentNew || conflict.Actual != model.IntentSubmitted {
		t.Errorf("conflict = %+v, want Expected=new Actual=submitted", conflict)
	}
}

func TestIntentRepo_UpdateState_TerminalIsImmutable(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	repo := IntentRepo{}
	seedIntent(t, db, decisionID, i.UID, "co-1")

	if err := repo.UpdateState(ctx, db, "co-1", model.IntentNew, model.IntentSubmitted, nowUTC()); err != nil {
		t.Fatalf("UpdateState(new->submitted): %v", err)
	}
	if err := repo.UpdateState(ctx, db, "co-1", model.IntentSubmitted, model.IntentFilled, nowUTC()); err != nil {
		t.Fatalf("UpdateState(submitted->filled): %v", err)
	}
	// filled is terminal: no edge leaves it, so any move is illegal, not merely
	// a conflict.
	err := repo.UpdateState(ctx, db, "co-1", model.IntentFilled, model.IntentSubmitted, nowUTC())
	var ill IllegalTransitionError
	if !errors.As(err, &ill) {
		t.Fatalf("UpdateState(filled->submitted) error = %v, want IllegalTransitionError", err)
	}
	got, err := repo.Get(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != model.IntentFilled {
		t.Errorf("terminal state changed to %s, want filled", got.State)
	}
}

// TestIntentRepo_SchemaRejectsInvalid proves the order_intents CHECK
// constraints reject rows the model would never produce but a bug or a bad
// migration might: a non-positive quantity and an unknown state token.
func TestIntentRepo_SchemaRejectsInvalid(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)

	insert := func(coID string, qty int, state string) error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO order_intents (client_order_id, decision_id, instrument_uid, side, qty, type, time_in_force, state, created_at, updated_at)
			VALUES (?, ?, ?, 'buy', ?, 'market', 'day', ?, ?, ?)`,
			coID, decisionID, string(i.UID), qty, state, timeText(nowUTC()), timeText(nowUTC()))
		return err
	}

	if err := insert("bad-qty", 0, "new"); err == nil {
		t.Error("expected CHECK violation for qty = 0, got nil")
	}
	if err := insert("bad-state", 1, "bogus"); err == nil {
		t.Error("expected CHECK violation for unknown state, got nil")
	}
	// A well-formed row still inserts.
	if err := insert("good", 1, "new"); err != nil {
		t.Errorf("valid intent insert failed: %v", err)
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

	if err := repo.UpdateState(ctx, db, "co-filled", model.IntentNew, model.IntentSubmitted, nowUTC()); err != nil {
		t.Fatalf("UpdateState submitted: %v", err)
	}
	if err := repo.UpdateState(ctx, db, "co-filled", model.IntentSubmitted, model.IntentFilled, nowUTC()); err != nil {
		t.Fatalf("UpdateState filled: %v", err)
	}
	if err := repo.UpdateState(ctx, db, "co-unknown", model.IntentNew, model.IntentUnknown, nowUTC()); err != nil {
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

func TestIntentRepo_UpdateStateWithReason(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	repo := IntentRepo{}
	seedIntent(t, db, decisionID, i.UID, "co-1")

	if err := repo.UpdateStateWithReason(ctx, db, "co-1", model.IntentNew, model.IntentRejected, nowUTC(), "unknown or invalid price tick"); err != nil {
		t.Fatalf("UpdateStateWithReason: %v", err)
	}
	got, err := repo.Get(ctx, db, "co-1")
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

func TestIntentRepo_UpdateStateWithReason_EmptyReasonRejected(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")

	if err := (IntentRepo{}).UpdateStateWithReason(ctx, db, "co-1", model.IntentNew, model.IntentRejected, nowUTC(), ""); err == nil {
		t.Fatal("expected an error for an empty reason, got nil")
	}
	// Nothing should have changed.
	got, err := (IntentRepo{}).Get(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != model.IntentNew {
		t.Errorf("state = %s, want new (unchanged)", got.State)
	}
}

func TestIntentRepo_UpdateState_LeavesReasonUntouched(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	repo := IntentRepo{}
	seedIntent(t, db, decisionID, i.UID, "co-1")

	// A plain UpdateState (no reason) must not clobber a NULL reason column,
	// and must leave it NULL going forward for the ordinary happy-path
	// transitions (new -> submitted -> acked) that never carry one.
	if err := repo.UpdateState(ctx, db, "co-1", model.IntentNew, model.IntentSubmitted, nowUTC()); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	got, err := repo.Get(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Reason != "" {
		t.Errorf("reason = %q, want empty", got.Reason)
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
