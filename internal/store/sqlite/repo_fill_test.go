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
	fills := []struct {
		fill        model.Fill
		lowFidelity bool
	}{
		{model.Fill{IntentID: "co-1", Price: model.MustDecimal("100"), Qty: 1, Fee: model.MustDecimal("0.05"), TS: base}, false},
		{model.Fill{IntentID: "co-1", Price: model.MustDecimal("100.5"), Qty: 2, Fee: model.MustDecimal("0.1"), TS: base.Add(time.Minute)}, true},
	}
	for n, f := range fills {
		if err := repo.Insert(ctx, db, f.fill, f.lowFidelity); err != nil {
			t.Fatalf("Insert fill %d: %v", n, err)
		}
	}

	list, err := repo.ListByIntent(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("ListByIntent: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].Price.String() != "100" || list[1].Price.String() != "100.5" {
		t.Errorf("list not ordered by ts: %s, %s", list[0].Price, list[1].Price)
	}
	if list[0].Fee.String() != "0.05" || list[1].Qty != 2 {
		t.Errorf("field round trip: %+v", list)
	}
	if list[0].LowFidelity {
		t.Errorf("list[0].LowFidelity = true, want false")
	}
	if !list[1].LowFidelity {
		t.Errorf("list[1].LowFidelity = false, want true")
	}
	if list[0].RealizedPnL != nil || list[1].RealizedPnL != nil {
		t.Errorf("RealizedPnL = %v, %v, want nil, nil (never set)", list[0].RealizedPnL, list[1].RealizedPnL)
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
