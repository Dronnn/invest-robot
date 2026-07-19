package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestFillRepo_InsertAndListByIntent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")
	repo := FillRepo{}

	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	fill := model.Fill{IntentID: "co-1", Price: model.MustDecimal("100.5"), Qty: 2, Fee: model.MustDecimal("0.1"), TS: base}
	if err := repo.Insert(ctx, db, fill, true); err != nil {
		t.Fatalf("Insert fill: %v", err)
	}

	list, err := repo.ListByIntent(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("ListByIntent: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1 (Phase 1 is full-fill-only: one fill row per intent)", len(list))
	}
	if list[0].Price.String() != "100.5" || list[0].Qty != 2 || list[0].Fee.String() != "0.1" {
		t.Errorf("field round trip: %+v", list[0])
	}
	if !list[0].LowFidelity {
		t.Errorf("list[0].LowFidelity = false, want true")
	}
	if list[0].RealizedPnL != nil {
		t.Errorf("RealizedPnL = %v, want nil (never set)", list[0].RealizedPnL)
	}
}

// TestFillRepo_Insert_OneFillPerIntent proves migration 2's
// UNIQUE(order_intent_id) index enforces the full-fill-only model:
// SetRealizedPnL updates by intent id alone, which is only unambiguous if an
// intent can never have a second fill row.
func TestFillRepo_Insert_OneFillPerIntent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")
	repo := FillRepo{}

	first := model.Fill{IntentID: "co-1", Price: model.MustDecimal("100"), Qty: 1, Fee: model.MustDecimal("0.05"), TS: nowUTC()}
	if err := repo.Insert(ctx, db, first, false); err != nil {
		t.Fatalf("Insert first fill: %v", err)
	}

	second := model.Fill{IntentID: "co-1", Price: model.MustDecimal("100.5"), Qty: 1, Fee: model.MustDecimal("0.05"), TS: nowUTC()}
	if err := repo.Insert(ctx, db, second, false); err == nil {
		t.Fatal("expected a second fill for the same intent to be rejected by the unique index, got nil error")
	}
}

func TestFillRepo_ListByIntent_Empty(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")

	list, err := (FillRepo{}).ListByIntent(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("ListByIntent: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("len(list) = %d, want 0", len(list))
	}
}

func TestFillRepo_SetRealizedPnL(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")
	repo := FillRepo{}

	fill := model.Fill{IntentID: "co-1", Price: model.MustDecimal("100"), Qty: 1, Fee: model.Decimal{}, TS: nowUTC()}
	if err := repo.Insert(ctx, db, fill, false); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	pnl := model.MustDecimal("-12.5")
	if err := repo.SetRealizedPnL(ctx, db, "co-1", pnl); err != nil {
		t.Fatalf("SetRealizedPnL: %v", err)
	}

	list, err := repo.ListByIntent(ctx, db, "co-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByIntent: err=%v len=%d", err, len(list))
	}
	if list[0].RealizedPnL == nil || list[0].RealizedPnL.String() != "-12.5" {
		t.Errorf("RealizedPnL = %v, want -12.5", list[0].RealizedPnL)
	}
}

func TestFillRepo_SetRealizedPnL_NotFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	err := (FillRepo{}).SetRealizedPnL(ctx, db, "no-such-intent", model.MustDecimal("1"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFillRepo_Recent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")
	seedIntent(t, db, decisionID, i.UID, "co-2")
	seedIntent(t, db, decisionID, i.UID, "co-3")
	repo := FillRepo{}

	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	for n, id := range []string{"co-1", "co-2", "co-3"} {
		f := model.Fill{IntentID: id, Price: model.MustDecimal("100"), Qty: 1, Fee: model.Decimal{}, TS: base.Add(time.Duration(n) * time.Minute)}
		if err := repo.Insert(ctx, db, f, false); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	all, err := repo.Recent(ctx, db, -1)
	if err != nil {
		t.Fatalf("Recent(-1): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
	if all[0].IntentID != "co-3" || all[2].IntentID != "co-1" {
		t.Errorf("Recent(-1) order = %s, %s, %s; want co-3, co-2, co-1", all[0].IntentID, all[1].IntentID, all[2].IntentID)
	}

	top2, err := repo.Recent(ctx, db, 2)
	if err != nil {
		t.Fatalf("Recent(2): %v", err)
	}
	if len(top2) != 2 || top2[0].IntentID != "co-3" || top2[1].IntentID != "co-2" {
		t.Errorf("Recent(2) = %+v, want co-3, co-2", top2)
	}
}
