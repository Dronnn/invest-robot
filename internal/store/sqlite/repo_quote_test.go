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

// TestQuoteRepo_Latest_SubsecondOrdering is the append-only counterpart to the
// timestamp-encoding fix: a quote one nanosecond newer than a whole-second
// quote must be the one Latest returns. Under the old variable-width encoding
// the subsecond value sorted before the whole-second one, so Latest returned
// the wrong row.
func TestQuoteRepo_Latest_SubsecondOrdering(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := QuoteRepo{}

	whole := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	older := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("100"), Ask: model.MustDecimal("100.5"), Last: model.MustDecimal("100"), TS: whole}
	newer := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("101"), Ask: model.MustDecimal("101.5"), Last: model.MustDecimal("101"), TS: whole.Add(time.Nanosecond)}

	// Insert the newer one first so row order cannot mask a bad ORDER BY.
	if err := repo.Insert(ctx, db, newer); err != nil {
		t.Fatalf("Insert newer: %v", err)
	}
	if err := repo.Insert(ctx, db, older); err != nil {
		t.Fatalf("Insert older: %v", err)
	}

	got, ok, err := repo.Latest(ctx, db, i.UID)
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if !got.TS.Equal(newer.TS) {
		t.Errorf("Latest().TS = %v, want the +1ns quote %v", got.TS, newer.TS)
	}
	if got.Bid.String() != "101" {
		t.Errorf("Latest().Bid = %s, want 101 (the newer quote)", got.Bid)
	}
}

// TestQuoteRepo_Latest_TieBreaksByID proves the id DESC tie-breaker makes
// "latest" deterministic when two rows share an identical timestamp: the
// most recently inserted (highest id) wins.
func TestQuoteRepo_Latest_TieBreaksByID(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := QuoteRepo{}

	ts := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	first := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("100"), Ask: model.MustDecimal("100.5"), Last: model.MustDecimal("100"), TS: ts}
	second := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("102"), Ask: model.MustDecimal("102.5"), Last: model.MustDecimal("102"), TS: ts}
	if err := repo.Insert(ctx, db, first); err != nil {
		t.Fatalf("Insert first: %v", err)
	}
	if err := repo.Insert(ctx, db, second); err != nil {
		t.Fatalf("Insert second: %v", err)
	}

	got, ok, err := repo.Latest(ctx, db, i.UID)
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if got.Bid.String() != "102" {
		t.Errorf("Latest().Bid = %s, want 102 (highest-id row on an equal-ts tie)", got.Bid)
	}
}

// TestQuoteRepo_LatestAsOf_Boundary proves the at-or-before-as_of contract
// the cycle orchestrator's assemble step depends on: a quote exactly at
// as_of is included (not excluded by a strict "<"), and a quote one
// nanosecond after as_of must not leak into the read.
func TestQuoteRepo_LatestAsOf_Boundary(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := QuoteRepo{}
	asOf := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	atAsOf := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("100"), Ask: model.MustDecimal("100.5"), Last: model.MustDecimal("100.2"), TS: asOf}
	after := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("200"), Ask: model.MustDecimal("200.5"), Last: model.MustDecimal("200.2"), TS: asOf.Add(time.Nanosecond)}
	if err := repo.Insert(ctx, db, atAsOf); err != nil {
		t.Fatalf("Insert at as_of: %v", err)
	}
	if err := repo.Insert(ctx, db, after); err != nil {
		t.Fatalf("Insert after as_of: %v", err)
	}

	got, ok, err := repo.LatestAsOf(ctx, db, i.UID, asOf)
	if err != nil {
		t.Fatalf("LatestAsOf: %v", err)
	}
	if !ok {
		t.Fatal("LatestAsOf: ok = false, want true")
	}
	if !got.TS.Equal(asOf) {
		t.Errorf("LatestAsOf(asOf).TS = %v, want %v (a quote exactly at as_of must be included)", got.TS, asOf)
	}
	if got.Bid.String() != "100" {
		t.Errorf("LatestAsOf(asOf).Bid = %s, want 100 (a quote after as_of must not leak)", got.Bid)
	}
}

// TestQuoteRepo_LatestAsOf_TieBreaksByID mirrors TestQuoteRepo_Latest_TieBreaksByID
// at the as-of boundary: when two quotes share the exact as_of timestamp, the
// most recently inserted (highest id) wins.
func TestQuoteRepo_LatestAsOf_TieBreaksByID(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := QuoteRepo{}
	ts := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	first := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("100"), Ask: model.MustDecimal("100.5"), Last: model.MustDecimal("100"), TS: ts}
	second := model.Quote{InstrumentUID: i.UID, Bid: model.MustDecimal("102"), Ask: model.MustDecimal("102.5"), Last: model.MustDecimal("102"), TS: ts}
	if err := repo.Insert(ctx, db, first); err != nil {
		t.Fatalf("Insert first: %v", err)
	}
	if err := repo.Insert(ctx, db, second); err != nil {
		t.Fatalf("Insert second: %v", err)
	}

	got, ok, err := repo.LatestAsOf(ctx, db, i.UID, ts)
	if err != nil || !ok {
		t.Fatalf("LatestAsOf: ok=%v err=%v", ok, err)
	}
	if got.Bid.String() != "102" {
		t.Errorf("LatestAsOf(ts).Bid = %s, want 102 (highest-id row on an equal-ts tie at the boundary)", got.Bid)
	}
}

func TestQuoteRepo_LatestAsOf_NoneFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")

	_, ok, err := (QuoteRepo{}).LatestAsOf(ctx, db, i.UID, time.Now())
	if err != nil {
		t.Fatalf("LatestAsOf: %v", err)
	}
	if ok {
		t.Error("LatestAsOf: ok = true, want false with no quotes")
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
