package sqlite

import (
	"context"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestOrderRepo_UpsertAndGet(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")
	repo := OrderRepo{}

	o := Order{
		ClientOrderID: "co-1",
		BrokerOrderID: "broker-123",
		Status:        "new",
		LotsExecuted:  0,
		UpdatedAt:     nowUTC(),
	}
	if err := repo.Upsert(ctx, db, o); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, ok, err := repo.Get(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want true")
	}
	if got.BrokerOrderID != "broker-123" || got.Status != "new" || got.ExecutedPrice != nil {
		t.Errorf("Get() = %+v, want broker-123/new/nil price", got)
	}

	price := model.MustDecimal("100.25")
	o.Status = "filled"
	o.LotsExecuted = 3
	o.ExecutedPrice = &price
	if err := repo.Upsert(ctx, db, o); err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}

	got, ok, err = repo.Get(ctx, db, "co-1")
	if err != nil || !ok {
		t.Fatalf("Get after update: ok=%v err=%v", ok, err)
	}
	if got.Status != "filled" || got.LotsExecuted != 3 {
		t.Errorf("Get() after update = %+v, want status=filled lots=3", got)
	}
	if got.ExecutedPrice == nil || got.ExecutedPrice.String() != "100.25" {
		t.Errorf("ExecutedPrice = %v, want 100.25", got.ExecutedPrice)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE client_order_id = ?`, "co-1").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("orders row count = %d, want 1 (upsert must not duplicate)", count)
	}
}

func TestOrderRepo_Get_NoneFound(t *testing.T) {
	db := openTest(t)
	_, ok, err := (OrderRepo{}).Get(context.Background(), db, "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("Get: ok = true, want false")
	}
}
