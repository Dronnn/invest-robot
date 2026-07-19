package sqlite

import (
	"context"
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
	fills := []model.Fill{
		{IntentID: "co-1", Price: model.MustDecimal("100"), Qty: 1, Fee: model.MustDecimal("0.05"), TS: base},
		{IntentID: "co-1", Price: model.MustDecimal("100.5"), Qty: 2, Fee: model.MustDecimal("0.1"), TS: base.Add(time.Minute)},
	}
	for n, f := range fills {
		if err := repo.Insert(ctx, db, f); err != nil {
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
