package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
)

// TestApplyFillAtomically exercises the transaction seam DESIGN §3 requires:
// "a fill and its portfolio effects commit in one SQLite transaction." This
// is the shape internal/portfolio will use in a later step.
func TestApplyFillAtomically(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")

	fill := model.Fill{
		IntentID: "co-1",
		Price:    model.MustDecimal("100"),
		Qty:      5,
		Fee:      model.MustDecimal("1.5"),
		TS:       nowUTC(),
	}

	applyFill := func(ctx context.Context, tx *sql.Tx) error {
		if err := (FillRepo{}).Insert(ctx, tx, fill); err != nil {
			return err
		}
		notional, err := fill.Price.MulInt(fill.Qty)
		if err != nil {
			return err
		}
		if err := (PositionRepo{}).Upsert(ctx, tx, model.Position{
			InstrumentUID: i.UID, Qty: fill.Qty, AvgPrice: fill.Price, UpdatedAt: fill.TS,
		}); err != nil {
			return err
		}
		delta, err := notional.Add(fill.Fee)
		if err != nil {
			return err
		}
		if _, err := (CashRepo{}).Insert(ctx, tx, CashEntry{
			TS: fill.TS, Delta: delta.Neg(), Currency: "rub", Reason: "fill", Ref: "co-1",
		}); err != nil {
			return err
		}
		return nil
	}

	if err := WithTx(ctx, db.DB, applyFill); err != nil {
		t.Fatalf("WithTx apply fill: %v", err)
	}

	fills, err := (FillRepo{}).ListByIntent(ctx, db, "co-1")
	if err != nil || len(fills) != 1 {
		t.Fatalf("ListByIntent: %v (len=%d)", err, len(fills))
	}
	pos, ok, err := (PositionRepo{}).Get(ctx, db, i.UID)
	if err != nil || !ok || pos.Qty != 5 {
		t.Fatalf("Get position: ok=%v err=%v pos=%+v", ok, err, pos)
	}
	balance, err := (CashRepo{}).Balance(ctx, db, "rub")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if balance.String() != "-501.5" {
		t.Errorf("Balance = %s, want -501.5", balance)
	}
}

// TestApplyFillAtomically_RollsBackAllOnFailure proves partial writes never
// survive: if the cash-ledger insert step fails, the fill and position
// writes from the same transaction must not be visible either.
func TestApplyFillAtomically_RollsBackAllOnFailure(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	cycleID := seedCycle(t, db)
	decisionID := seedDecision(t, db, cycleID, i.UID)
	seedIntent(t, db, decisionID, i.UID, "co-1")

	fill := model.Fill{IntentID: "co-1", Price: model.MustDecimal("100"), Qty: 5, Fee: model.MustDecimal("1.5"), TS: nowUTC()}

	failing := func(ctx context.Context, tx *sql.Tx) error {
		if err := (FillRepo{}).Insert(ctx, tx, fill); err != nil {
			return err
		}
		if err := (PositionRepo{}).Upsert(ctx, tx, model.Position{
			InstrumentUID: i.UID, Qty: fill.Qty, AvgPrice: fill.Price, UpdatedAt: fill.TS,
		}); err != nil {
			return err
		}
		return fmt.Errorf("simulated cash-ledger failure")
	}

	if err := WithTx(ctx, db.DB, failing); err == nil {
		t.Fatal("expected WithTx to return the injected error")
	}

	fills, err := (FillRepo{}).ListByIntent(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("ListByIntent: %v", err)
	}
	if len(fills) != 0 {
		t.Errorf("len(fills) = %d, want 0 (fill insert must have rolled back)", len(fills))
	}
	_, ok, err := (PositionRepo{}).Get(ctx, db, i.UID)
	if err != nil {
		t.Fatalf("Get position: %v", err)
	}
	if ok {
		t.Error("position exists, want none (position upsert must have rolled back)")
	}
}
