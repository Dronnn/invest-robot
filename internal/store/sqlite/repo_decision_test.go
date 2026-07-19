package sqlite

import (
	"context"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestDecisionRepo_InsertAndListByCycle(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	repo := DecisionRepo{}

	limit := model.MustDecimal("101.5")
	rec := DecisionRecord{
		CycleID: cycleID,
		Decision: model.Decision{
			InstrumentUID: i.UID,
			Action:        model.ActionBuy,
			Quantity:      3,
			OrderType:     model.OrderLimit,
			LimitPrice:    &limit,
			TimeInForce:   model.TIFDay,
			Rationale:     "momentum breakout",
			Confidence:    0.82,
		},
		RawResponse:      `{"action":"buy"}`,
		ValidationStatus: "valid",
	}
	id, err := repo.Insert(ctx, db, rec)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	list, err := repo.ListByCycle(ctx, db, cycleID)
	if err != nil {
		t.Fatalf("ListByCycle: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	got := list[0]
	if got.ID != id {
		t.Errorf("ID = %d, want %d", got.ID, id)
	}
	if got.Decision.Action != model.ActionBuy || got.Decision.Quantity != 3 || got.Decision.OrderType != model.OrderLimit {
		t.Errorf("Decision = %+v, want action=buy qty=3 type=limit", got.Decision)
	}
	if got.Decision.LimitPrice == nil || got.Decision.LimitPrice.String() != "101.5" {
		t.Errorf("LimitPrice = %v, want 101.5", got.Decision.LimitPrice)
	}
	if got.RawResponse != rec.RawResponse {
		t.Errorf("RawResponse = %q, want %q", got.RawResponse, rec.RawResponse)
	}
	if got.ValidationStatus != "valid" {
		t.Errorf("ValidationStatus = %q, want valid", got.ValidationStatus)
	}
}

func TestDecisionRepo_NilLimitPriceForMarketOrder(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)

	rec := DecisionRecord{
		CycleID: cycleID,
		Decision: model.Decision{
			InstrumentUID: i.UID,
			Action:        model.ActionHold,
			OrderType:     model.OrderMarket,
			TimeInForce:   model.TIFIOC,
			Rationale:     "no signal",
			Confidence:    0.1,
		},
		ValidationStatus: "valid",
	}
	if _, err := (DecisionRepo{}).Insert(ctx, db, rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	list, err := (DecisionRepo{}).ListByCycle(ctx, db, cycleID)
	if err != nil {
		t.Fatalf("ListByCycle: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].Decision.LimitPrice != nil {
		t.Errorf("LimitPrice = %v, want nil for a market order", list[0].Decision.LimitPrice)
	}
	if list[0].RawResponse != "" {
		t.Errorf("RawResponse = %q, want empty when not supplied", list[0].RawResponse)
	}
}

func TestDecisionRepo_ListByCycle_Empty(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	cycleID := seedCycle(t, db)

	list, err := (DecisionRepo{}).ListByCycle(ctx, db, cycleID)
	if err != nil {
		t.Fatalf("ListByCycle: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("len(list) = %d, want 0", len(list))
	}
}
