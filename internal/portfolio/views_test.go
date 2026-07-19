package portfolio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

func TestSummary_Shape(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 10).UID
	seedIntent(t, db, uid, "co-1")
	p, sim := newTestPortfolio()

	if err := p.Init(ctx, db, mustDecimal(t, "10000")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: model.Decimal{}, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 10,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}

	quotes := map[model.InstrumentUID]model.Quote{uid: {InstrumentUID: uid, Last: mustDecimal(t, "120"), TS: sim.Now()}}
	sum, err := p.Summary(ctx, db, quotes)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Cash.String() != "5000" {
		t.Errorf("cash = %s, want 5000", sum.Cash)
	}
	if sum.Equity.String() != "11000" {
		t.Errorf("equity = %s, want 11000", sum.Equity)
	}
	if len(sum.Positions) != 1 {
		t.Fatalf("len(positions) = %d, want 1", len(sum.Positions))
	}
	pv := sum.Positions[0]
	if pv.UID != uid || pv.Qty != 5 || pv.AvgPrice.String() != "100" || pv.LastPrice.String() != "120" || pv.UnrealizedPnL.String() != "1000" {
		t.Errorf("position view = %+v, want UID=%s Qty=5 AvgPrice=100 LastPrice=120 UnrealizedPnL=1000", pv, uid)
	}

	// Summary must not persist an equity snapshot (that's MarkToMarket's job).
	if _, ok, err := (sqlite.EquityRepo{}).Latest(ctx, db); err != nil || ok {
		t.Errorf("latest snapshot after Summary: ok=%v err=%v, want none", ok, err)
	}
}

func TestSummary_MissingQuoteErrors(t *testing.T) {
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

	_, err := p.Summary(ctx, db, map[model.InstrumentUID]model.Quote{})
	var missing *MissingQuoteError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *MissingQuoteError", err)
	}
}

func TestDayPnL_WithoutSessionSnapshotErrors(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	p, sim := newTestPortfolio()

	if err := p.Init(ctx, db, mustDecimal(t, "1000")); err != nil {
		t.Fatalf("init: %v", err)
	}
	_, err := p.DayPnL(ctx, db, sim.Now())
	if !errors.Is(err, ErrSessionSnapshotMissing) {
		t.Fatalf("err = %v, want ErrSessionSnapshotMissing", err)
	}
}

func TestDayPnL_RealizedAndUnrealizedSplit(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 1).UID
	seedIntent(t, db, uid, "buy-1")
	seedIntent(t, db, uid, "sell-1")
	p, sim := newTestPortfolio()
	sessionStart := sim.Now()

	if err := p.Init(ctx, db, mustDecimal(t, "10000")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := p.EnsureSessionStartSnapshot(ctx, db, sessionStart); err != nil {
		t.Fatalf("ensure session start snapshot: %v", err)
	}

	sim.Advance(time.Hour)

	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "buy-1", Price: mustDecimal(t, "100"), Qty: 10, Fee: model.Decimal{}, TS: sim.Now()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "sell-1", Price: mustDecimal(t, "120"), Qty: 10, Fee: model.Decimal{}, TS: sim.Now()},
		InstrumentUID: uid, Side: model.SideSell, Lot: 1,
	}); err != nil {
		t.Fatalf("sell: %v", err)
	}

	if _, err := p.MarkToMarket(ctx, db, map[model.InstrumentUID]model.Quote{}, sim.Now()); err != nil {
		t.Fatalf("mark to market: %v", err)
	}

	result, err := p.DayPnL(ctx, db, sessionStart)
	if err != nil {
		t.Fatalf("day pnl: %v", err)
	}
	if result.Realized.String() != "200" {
		t.Errorf("realized = %s, want 200", result.Realized)
	}
	if !result.Unrealized.IsZero() {
		t.Errorf("unrealized = %s, want 0 (position fully closed, no open mark)", result.Unrealized)
	}
	if result.Total.String() != "200" {
		t.Errorf("total = %s, want 200", result.Total)
	}
}

func TestDayPnL_FeesReportedSeparatelyNotAsUnrealized(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 1).UID
	seedIntent(t, db, uid, "buy-1")
	seedIntent(t, db, uid, "sell-1")
	p, sim := newTestPortfolio()
	sessionStart := sim.Now()

	if err := p.Init(ctx, db, mustDecimal(t, "10000")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := p.EnsureSessionStartSnapshot(ctx, db, sessionStart); err != nil {
		t.Fatalf("ensure session start snapshot: %v", err)
	}

	sim.Advance(time.Hour)

	// A flat round-trip at the same price with a 5 fee on each leg: gross
	// realized PnL is 0, but the account paid 10 in commissions.
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "buy-1", Price: mustDecimal(t, "100"), Qty: 10, Fee: mustDecimal(t, "5"), TS: sim.Now()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "sell-1", Price: mustDecimal(t, "100"), Qty: 10, Fee: mustDecimal(t, "5"), TS: sim.Now()},
		InstrumentUID: uid, Side: model.SideSell, Lot: 1,
	}); err != nil {
		t.Fatalf("sell: %v", err)
	}

	if _, err := p.MarkToMarket(ctx, db, map[model.InstrumentUID]model.Quote{}, sim.Now()); err != nil {
		t.Fatalf("mark to market: %v", err)
	}

	result, err := p.DayPnL(ctx, db, sessionStart)
	if err != nil {
		t.Fatalf("day pnl: %v", err)
	}
	// Gross realized is 0 (flat round-trip); fees are their own 10; unrealized
	// must be 0 (position fully closed), NOT the -10 that the fee outflow would
	// masquerade as if it were folded into unrealized.
	if result.Realized.String() != "0" {
		t.Errorf("realized = %s, want 0", result.Realized)
	}
	if result.Fees.String() != "10" {
		t.Errorf("fees = %s, want 10", result.Fees)
	}
	if !result.Unrealized.IsZero() {
		t.Errorf("unrealized = %s, want 0 (fees must not surface as unrealized loss)", result.Unrealized)
	}
	if result.Total.String() != "-10" {
		t.Errorf("total = %s, want -10 (net of the two 5 commissions)", result.Total)
	}
}

func TestEnsureSessionStartSnapshot_IdempotentAndRollsForward(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	p, sim := newTestPortfolio()
	day1 := sim.Now()

	if err := p.Init(ctx, db, mustDecimal(t, "1000")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := p.EnsureSessionStartSnapshot(ctx, db, day1); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := p.EnsureSessionStartSnapshot(ctx, db, day1); err != nil {
		t.Fatalf("second ensure (idempotent): %v", err)
	}
	snaps, err := (sqlite.EquityRepo{}).Range(ctx, db, day1, day1)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("len(snaps) after two calls at the same instant = %d, want 1", len(snaps))
	}

	// Roll forward into a new session: cash changes, then a new session
	// boundary should carry the last known total forward unchanged.
	sim.Advance(24 * time.Hour)
	day2 := sim.Now()
	if err := p.Init(ctx, db, mustDecimal(t, "999999")); err != nil { // no-op, already seeded
		t.Fatalf("init no-op: %v", err)
	}
	if err := p.EnsureSessionStartSnapshot(ctx, db, day2); err != nil {
		t.Fatalf("day2 ensure: %v", err)
	}
	day2Snaps, err := (sqlite.EquityRepo{}).Range(ctx, db, day2, day2)
	if err != nil {
		t.Fatalf("range day2: %v", err)
	}
	if len(day2Snaps) != 1 {
		t.Fatalf("len(day2Snaps) = %d, want 1", len(day2Snaps))
	}
	if day2Snaps[0].Total.String() != "1000" {
		t.Errorf("day2 snapshot total = %s, want 1000 (rolled forward from day1)", day2Snaps[0].Total)
	}
}

func TestEnsureSessionStartSnapshot_CannotEstablishWithOpenPositionsAndNoPriorSnapshot(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 1).UID
	seedIntent(t, db, uid, "co-1")
	p, sim := newTestPortfolio()

	if err := applyFillViaExecution(t, ctx, p, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: model.Decimal{}, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}

	err := p.EnsureSessionStartSnapshot(ctx, db, sim.Now())
	if !errors.Is(err, ErrCannotEstablishSessionSnapshot) {
		t.Fatalf("err = %v, want ErrCannotEstablishSessionSnapshot", err)
	}
}
