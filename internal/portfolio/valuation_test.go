package portfolio

import (
	"context"
	"errors"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

func TestMarkToMarket_ValuesPositionsAndPersistsSnapshot(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 10).UID
	seedIntent(t, db, uid, "co-1")
	p, sim := newTestPortfolio()

	if err := p.Init(ctx, db, mustDecimal(t, "10000")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := p.ApplyFill(ctx, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: model.Decimal{}, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 10,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}

	quotes := map[model.InstrumentUID]model.Quote{
		uid: {InstrumentUID: uid, Last: mustDecimal(t, "120"), TS: sim.Now()},
	}
	eq, err := p.MarkToMarket(ctx, db, quotes, sim.Now())
	if err != nil {
		t.Fatalf("mark to market: %v", err)
	}
	if eq.Cash.String() != "5000" {
		t.Errorf("cash = %s, want 5000", eq.Cash)
	}
	if eq.MarketValue.String() != "6000" {
		t.Errorf("market value = %s, want 6000 (5 lots * 10 shares/lot * 120)", eq.MarketValue)
	}
	if eq.Total.String() != "11000" {
		t.Errorf("total = %s, want 11000", eq.Total)
	}

	latest, ok, err := (sqlite.EquityRepo{}).Latest(ctx, db)
	if err != nil || !ok {
		t.Fatalf("latest snapshot: ok=%v err=%v", ok, err)
	}
	if latest.Total.String() != "11000" {
		t.Errorf("persisted snapshot total = %s, want 11000", latest.Total)
	}
}

func TestMarkToMarket_MissingQuoteErrors(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1", 1).UID
	seedIntent(t, db, uid, "co-1")
	p, sim := newTestPortfolio()

	if err := p.ApplyFill(ctx, db, FillApplication{
		Fill:          model.Fill{IntentID: "co-1", Price: mustDecimal(t, "100"), Qty: 5, Fee: model.Decimal{}, TS: nowUTC()},
		InstrumentUID: uid, Side: model.SideBuy, Lot: 1,
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}

	// No quote at all for the held instrument.
	_, err := p.MarkToMarket(ctx, db, map[model.InstrumentUID]model.Quote{}, sim.Now())
	var missing *MissingQuoteError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *MissingQuoteError", err)
	}
	if len(missing.Instruments) != 1 || missing.Instruments[0] != uid {
		t.Errorf("missing instruments = %v, want [%s]", missing.Instruments, uid)
	}

	// A quote with a zero Last is treated the same as no quote.
	_, err = p.MarkToMarket(ctx, db, map[model.InstrumentUID]model.Quote{uid: {InstrumentUID: uid}}, sim.Now())
	if !errors.As(err, &missing) {
		t.Fatalf("err (zero last) = %v, want *MissingQuoteError", err)
	}

	// No snapshot should have been persisted by either failed attempt.
	if _, ok, err := (sqlite.EquityRepo{}).Latest(ctx, db); err != nil || ok {
		t.Errorf("latest snapshot: ok=%v err=%v, want no snapshot persisted", ok, err)
	}
}

func TestMarkToMarket_NoPositionsIsCashOnly(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	p, sim := newTestPortfolio()

	if err := p.Init(ctx, db, mustDecimal(t, "500")); err != nil {
		t.Fatalf("init: %v", err)
	}
	eq, err := p.MarkToMarket(ctx, db, map[model.InstrumentUID]model.Quote{}, sim.Now())
	if err != nil {
		t.Fatalf("mark to market: %v", err)
	}
	if eq.Cash.String() != "500" || !eq.MarketValue.IsZero() || eq.Total.String() != "500" {
		t.Errorf("equity = %+v, want cash=500 marketValue=0 total=500", eq)
	}
}
