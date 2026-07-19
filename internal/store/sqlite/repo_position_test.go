package sqlite

import (
	"context"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestPositionRepo_UpsertGetList(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i1 := seedInstrument(t, db, "uid-1")
	i2 := seedInstrument(t, db, "uid-2")
	repo := PositionRepo{}

	p1 := model.Position{InstrumentUID: i1.UID, Qty: 10, AvgPrice: model.MustDecimal("100"), UpdatedAt: nowUTC()}
	p2 := model.Position{InstrumentUID: i2.UID, Qty: 5, AvgPrice: model.MustDecimal("50.25"), UpdatedAt: nowUTC()}
	if err := repo.Upsert(ctx, db, p1); err != nil {
		t.Fatalf("Upsert p1: %v", err)
	}
	if err := repo.Upsert(ctx, db, p2); err != nil {
		t.Fatalf("Upsert p2: %v", err)
	}

	got, ok, err := repo.Get(ctx, db, i1.UID)
	if err != nil || !ok {
		t.Fatalf("Get p1: ok=%v err=%v", ok, err)
	}
	if got.Qty != 10 || got.AvgPrice.String() != "100" {
		t.Errorf("Get(p1) = %+v, want qty=10 avg=100", got)
	}

	p1.Qty = 15
	p1.AvgPrice = model.MustDecimal("102.5")
	if err := repo.Upsert(ctx, db, p1); err != nil {
		t.Fatalf("Upsert (update) p1: %v", err)
	}
	got, ok, err = repo.Get(ctx, db, i1.UID)
	if err != nil || !ok {
		t.Fatalf("Get p1 after update: ok=%v err=%v", ok, err)
	}
	if got.Qty != 15 || got.AvgPrice.String() != "102.5" {
		t.Errorf("Get(p1) after update = %+v, want qty=15 avg=102.5", got)
	}

	list, err := repo.List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(List) = %d, want 2 (upsert must not duplicate)", len(list))
	}
}

func TestPositionRepo_Get_NoneFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")

	_, ok, err := (PositionRepo{}).Get(ctx, db, i.UID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("Get: ok = true, want false")
	}
}
