package portfolio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

func newTestPortfolio() (*Portfolio, *clock.Simulated) {
	sim := clock.NewSimulated(time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC))
	return New(sim, testCurrency), sim
}

func mustDecimal(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseDecimal(s)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", s, err)
	}
	return d
}

func TestApplyFill_BuyWeightedAveragePrice(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 10).UID
	seedIntent(t, db, uid, "co-1")
	seedIntent(t, db, uid, "co-2")
	p, _ := newTestPortfolio()

	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: mustDecimal(t, "1.5"), TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 10,
	}); err != nil {
		t.Fatalf("first buy: %v", err)
	}
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-2", Price: mustDecimal(t, "110"), Qty: 5, Fee: mustDecimal(t, "2"), TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 10,
	}); err != nil {
		t.Fatalf("second buy: %v", err)
	}

	pos, ok, err := (sqlite.PositionRepo{}).Get(ctx, db, uid)
	if err != nil || !ok {
		t.Fatalf("get position: ok=%v err=%v", ok, err)
	}
	if pos.Qty != 10 {
		t.Errorf("qty = %d, want 10", pos.Qty)
	}
	if pos.AvgPrice.String() != "105" {
		t.Errorf("avg price = %s, want 105", pos.AvgPrice)
	}

	balance, err := (sqlite.CashRepo{}).Balance(ctx, db, testCurrency)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	// buy1: -(100*50) - 1.5 = -5001.5; buy2: -(110*50) - 2 = -5502
	if balance.String() != "-10503.5" {
		t.Errorf("cash balance = %s, want -10503.5", balance)
	}
}

func TestApplyFill_FeeLedgerEntriesAlwaysWritten(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 1).UID
	seedIntent(t, db, uid, "co-1")
	p, _ := newTestPortfolio()

	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: model.Decimal{}, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}

	entries, err := (sqlite.CashRepo{}).Recent(ctx, db, -1)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	var fillCount, feeCount int
	for _, e := range entries {
		if e.Ref != "co-1" {
			continue
		}
		switch e.Reason {
		case reasonFill:
			fillCount++
		case reasonFee:
			feeCount++
			if !e.Delta.IsZero() {
				t.Errorf("fee delta = %s, want 0 (fee was zero)", e.Delta)
			}
		}
	}
	if fillCount != 1 || feeCount != 1 {
		t.Errorf("fillCount=%d feeCount=%d, want 1 and 1 (fee row must exist even when fee is zero)", fillCount, feeCount)
	}
}

func TestApplyFill_SellPartialThenFullClose(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-2", 1).UID
	seedIntent(t, db, uid, "buy-1")
	seedIntent(t, db, uid, "sell-1")
	seedIntent(t, db, uid, "sell-2")
	p, _ := newTestPortfolio()

	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "buy-1", Price: mustDecimal(t, "100"), Qty: 10, Fee: model.Decimal{}, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}

	// Partial sell: 4 of 10 lots at 120, realized pnl = (120-100)*4 = 80.
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "sell-1", Price: mustDecimal(t, "120"), Qty: 4, Fee: mustDecimal(t, "1"), TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideSell, Lot: 1,
	}); err != nil {
		t.Fatalf("partial sell: %v", err)
	}
	pos, ok, err := (sqlite.PositionRepo{}).Get(ctx, db, uid)
	if err != nil || !ok {
		t.Fatalf("get position after partial sell: ok=%v err=%v", ok, err)
	}
	if pos.Qty != 6 {
		t.Errorf("qty after partial sell = %d, want 6", pos.Qty)
	}
	if pos.AvgPrice.String() != "100" {
		t.Errorf("avg price after partial sell = %s, want 100 (unchanged by a sell)", pos.AvgPrice)
	}
	if pnl := realizedPnLFor(t, ctx, db, "sell-1"); pnl.String() != "80" {
		t.Errorf("realized pnl (partial) = %s, want 80", pnl)
	}

	balance, err := (sqlite.CashRepo{}).Balance(ctx, db, testCurrency)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	// buy: -1000; sell: +480 - 1 = +479. Total: -521.
	if balance.String() != "-521" {
		t.Errorf("cash balance after partial sell = %s, want -521", balance)
	}

	// Full close: remaining 6 lots at 90, realized pnl = (90-100)*6 = -60.
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "sell-2", Price: mustDecimal(t, "90"), Qty: 6, Fee: mustDecimal(t, "0.5"), TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideSell, Lot: 1,
	}); err != nil {
		t.Fatalf("full close sell: %v", err)
	}
	pos, ok, err = (sqlite.PositionRepo{}).Get(ctx, db, uid)
	if err != nil || !ok {
		t.Fatalf("get position after full close: ok=%v err=%v", ok, err)
	}
	if pos.Qty != 0 {
		t.Errorf("qty after full close = %d, want 0", pos.Qty)
	}
	if !pos.AvgPrice.IsZero() {
		t.Errorf("avg price after full close = %s, want 0 (reset)", pos.AvgPrice)
	}
	if pnl := realizedPnLFor(t, ctx, db, "sell-2"); pnl.String() != "-60" {
		t.Errorf("realized pnl (full close) = %s, want -60", pnl)
	}

	balance, err = (sqlite.CashRepo{}).Balance(ctx, db, testCurrency)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	// -521 + 540 - 0.5 = 18.5
	if balance.String() != "18.5" {
		t.Errorf("cash balance after full close = %s, want 18.5", balance)
	}
}

// realizedPnLFor reads back fills.realized_pnl for intentID. Phase 1 assumes
// full fills, so exactly one fill row is expected per intent.
func realizedPnLFor(t *testing.T, ctx context.Context, db *sqlite.DB, intentID string) model.Decimal {
	t.Helper()
	fills, err := (sqlite.FillRepo{}).ListByIntent(ctx, db, intentID)
	if err != nil {
		t.Fatalf("ListByIntent %s: %v", intentID, err)
	}
	if len(fills) != 1 {
		t.Fatalf("len(fills) for %s = %d, want 1", intentID, len(fills))
	}
	if fills[0].RealizedPnL == nil {
		t.Fatalf("fills.realized_pnl for %s is nil, want a value", intentID)
	}
	return *fills[0].RealizedPnL
}

func TestApplyFill_OversellRejected(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-3", 1).UID
	seedIntent(t, db, uid, "buy-1")
	seedIntent(t, db, uid, "sell-1")
	p, _ := newTestPortfolio()

	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "buy-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: model.Decimal{}, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}

	// Mirror the real call order end to end: execution inserts the fills
	// row and calls ApplyFill inside one transaction, so an oversell must
	// roll both back together, not just leave ApplyFill's own writes
	// unwritten.
	oversellFill := model.Fill{IntentID: "sell-1", Price: mustDecimal(t, "100"), Qty: 6, Fee: model.Decimal{}, TS: nowUTC()}
	err := sqlite.WithTx(ctx, db.DB, func(ctx context.Context, tx *sql.Tx) error {
		if err := (sqlite.FillRepo{}).Insert(ctx, tx, oversellFill, false); err != nil {
			return err
		}
		return p.ApplyFill(ctx, tx, FillApplication{
			Fill: oversellFill, InstrumentUID: uid, Side: model.SideSell, Lot: 1,
		})
	})
	var oversell *OversellError
	if !errors.As(err, &oversell) {
		t.Fatalf("err = %v, want *OversellError", err)
	}
	if oversell.Have != 5 || oversell.Want != 6 {
		t.Errorf("oversell = %+v, want Have=5 Want=6", oversell)
	}

	// Nothing from the rejected sell should have survived, including the
	// fills row execution would have inserted moments before.
	pos, ok, err := (sqlite.PositionRepo{}).Get(ctx, db, uid)
	if err != nil || !ok || pos.Qty != 5 {
		t.Fatalf("position after rejected oversell: ok=%v err=%v qty=%d, want qty=5", ok, err, pos.Qty)
	}
	fills, err := (sqlite.FillRepo{}).ListByIntent(ctx, db, "sell-1")
	if err != nil || len(fills) != 0 {
		t.Fatalf("fills for rejected sell-1: err=%v len=%d, want 0 (must have rolled back)", err, len(fills))
	}
}

func TestApplyFill_ZeroPositionResetsAvgPriceForReentry(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-4", 1).UID
	seedIntent(t, db, uid, "buy-1")
	seedIntent(t, db, uid, "sell-1")
	seedIntent(t, db, uid, "buy-2")
	p, _ := newTestPortfolio()

	steps := []struct {
		intent string
		side   model.Side
		price  string
		qty    int64
	}{
		{"buy-1", model.SideBuy, "100", 5},
		{"sell-1", model.SideSell, "150", 5}, // full close
		{"buy-2", model.SideBuy, "80", 3},    // re-entry
	}
	for _, s := range steps {
		if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
			Fill:          model.Fill{IntentID: s.intent, Price: mustDecimal(t, s.price), Qty: s.qty, Fee: model.Decimal{}, TS: nowUTC()},
			InstrumentUID: uid, Side: s.side, Lot: 1,
		}); err != nil {
			t.Fatalf("%s: %v", s.intent, err)
		}
	}

	pos, ok, err := (sqlite.PositionRepo{}).Get(ctx, db, uid)
	if err != nil || !ok {
		t.Fatalf("get position: ok=%v err=%v", ok, err)
	}
	if pos.Qty != 3 {
		t.Errorf("qty = %d, want 3", pos.Qty)
	}
	if pos.AvgPrice.String() != "80" {
		t.Errorf("avg price = %s, want 80 (re-entry after full close must not inherit the old avg price)", pos.AvgPrice)
	}
}

func TestApplyFill_InvalidFillRejected(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-5", 1).UID
	p, _ := newTestPortfolio()

	cases := []struct {
		name string
		fa   FillApplication
	}{
		{"bad side", FillApplication{Fill: model.Fill{IntentID: "x", Price: mustDecimal(t, "1"), Qty: 1}, InstrumentUID: uid, Side: "sideways", Lot: 1}},
		{"zero qty", FillApplication{Fill: model.Fill{IntentID: "x", Price: mustDecimal(t, "1"), Qty: 0}, InstrumentUID: uid, Side: model.SideBuy, Lot: 1}},
		{"zero lot", FillApplication{Fill: model.Fill{IntentID: "x", Price: mustDecimal(t, "1"), Qty: 1}, InstrumentUID: uid, Side: model.SideBuy, Lot: 0}},
		{"negative price", FillApplication{Fill: model.Fill{IntentID: "x", Price: mustDecimal(t, "-1"), Qty: 1}, InstrumentUID: uid, Side: model.SideBuy, Lot: 1}},
		{"negative fee", FillApplication{Fill: model.Fill{IntentID: "x", Price: mustDecimal(t, "1"), Qty: 1, Fee: mustDecimal(t, "-1")}, InstrumentUID: uid, Side: model.SideBuy, Lot: 1}},
		{"empty intent id", FillApplication{Fill: model.Fill{IntentID: "", Price: mustDecimal(t, "1"), Qty: 1}, InstrumentUID: uid, Side: model.SideBuy, Lot: 1}},
		{"empty instrument uid", FillApplication{Fill: model.Fill{IntentID: "x", Price: mustDecimal(t, "1"), Qty: 1}, InstrumentUID: "", Side: model.SideBuy, Lot: 1}},
		{"currency mismatch", FillApplication{Fill: model.Fill{IntentID: "x", Price: mustDecimal(t, "1"), Qty: 1}, InstrumentUID: uid, Side: model.SideBuy, Lot: 1, Currency: "USD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var invalid *InvalidFillError
			if err := p.ApplyFill(ctx, db, tc.fa); !errors.As(err, &invalid) {
				t.Errorf("err = %v, want *InvalidFillError", err)
			}
		})
	}
}

func TestApplyFill_MatchingCurrencyPasses(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-cur", 1).UID
	seedIntent(t, db, uid, "co-1")
	p, _ := newTestPortfolio()

	// testCurrency is "rub"; a fill tagged "RUB" (case-insensitive) must post
	// cleanly.
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 1, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1, Currency: "RUB",
	}); err != nil {
		t.Fatalf("matching currency fill should apply: %v", err)
	}
}

func TestApplyFill_WithTxRollbackLeavesNoPartialRows(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-6", 1).UID
	seedIntent(t, db, uid, "co-1")
	p, _ := newTestPortfolio()

	fill := model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: mustDecimal(t, "1"), TS: nowUTC()}
	injected := fmt.Errorf("injected failure after ApplyFill")
	err := sqlite.WithTx(ctx, db.DB, func(ctx context.Context, tx *sql.Tx) error {
		// Mirror the real call order: execution inserts the fills row, then
		// calls ApplyFill, both inside the same transaction.
		if err := (sqlite.FillRepo{}).Insert(ctx, tx, fill, false); err != nil {
			return err
		}
		if err := p.ApplyFill(ctx, tx, FillApplication{
			Fill: fill, InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
		}); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("WithTx err = %v, want the injected error", err)
	}

	fills, err := (sqlite.FillRepo{}).ListByIntent(ctx, db, "co-1")
	if err != nil {
		t.Fatalf("ListByIntent: %v", err)
	}
	if len(fills) != 0 {
		t.Errorf("len(fills) = %d, want 0 (fill insert must have rolled back)", len(fills))
	}
	_, ok, err := (sqlite.PositionRepo{}).Get(ctx, db, uid)
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if ok {
		t.Error("position exists, want none (position upsert must have rolled back)")
	}
	balance, err := (sqlite.CashRepo{}).Balance(ctx, db, testCurrency)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if !balance.IsZero() {
		t.Errorf("cash balance = %s, want 0 (cash ledger inserts must have rolled back)", balance)
	}
}

func TestInit_SeedsStartingCashOnce(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	p, _ := newTestPortfolio()

	if err := p.Init(ctx, db, mustDecimal(t, "100000")); err != nil {
		t.Fatalf("first init: %v", err)
	}
	balance, err := (sqlite.CashRepo{}).Balance(ctx, db, testCurrency)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.String() != "100000" {
		t.Errorf("balance after init = %s, want 100000", balance)
	}

	// A second Init (even with a different amount) must be a no-op.
	if err := p.Init(ctx, db, mustDecimal(t, "999")); err != nil {
		t.Fatalf("second init: %v", err)
	}
	balance, err = (sqlite.CashRepo{}).Balance(ctx, db, testCurrency)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.String() != "100000" {
		t.Errorf("balance after second init = %s, want unchanged 100000", balance)
	}

	entries, err := (sqlite.CashRepo{}).Recent(ctx, db, -1)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("len(entries) = %d, want 1 (init must not have written twice)", len(entries))
	}
}
