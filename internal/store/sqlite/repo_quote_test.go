package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestQuoteRepo_InsertAndLatest(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := QuoteRepo{}

	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	for n := 0; n < 3; n++ {
		q := model.Quote{
			InstrumentUID: i.UID,
			Bid:           model.MustDecimal("100"),
			Ask:           model.MustDecimal("100.5"),
			Last:          model.MustDecimal("100.2"),
			TS:            base.Add(time.Duration(n) * time.Minute),
		}
		if err := repo.Insert(ctx, db, q); err != nil {
			t.Fatalf("Insert quote %d: %v", n, err)
		}
	}

	got, ok, err := repo.Latest(ctx, db, i.UID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !ok {
		t.Fatal("Latest: ok = false, want true")
	}
	if !got.TS.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("Latest().TS = %v, want the most recent inserted quote", got.TS)
	}
	if got.Bid.String() != "100" || got.Ask.String() != "100.5" {
		t.Errorf("decimal round trip: Bid=%s Ask=%s", got.Bid, got.Ask)
	}
}

func TestQuoteRepo_Latest_NoneFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")

	_, ok, err := (QuoteRepo{}).Latest(ctx, db, i.UID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if ok {
		t.Error("Latest: ok = true, want false with no quotes")
	}
}

func TestQuote_HasBidAsk(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	q := model.Quote{InstrumentUID: i.UID, Last: model.MustDecimal("100"), TS: time.Now()}
	if err := (QuoteRepo{}).Insert(ctx, db, q); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := (QuoteRepo{}).Latest(ctx, db, i.UID)
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if got.HasBidAsk() {
		t.Error("HasBidAsk() = true for a quote with zero bid/ask, want false")
	}
}
