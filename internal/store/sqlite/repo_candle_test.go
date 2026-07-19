package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

func testCandle(uid model.InstrumentUID, ts time.Time, complete bool) model.Candle {
	return model.Candle{
		InstrumentUID: uid,
		Interval:      model.Interval5m,
		Open:          model.MustDecimal("100.5"),
		High:          model.MustDecimal("101.25"),
		Low:           model.MustDecimal("99.75"),
		Close:         model.MustDecimal("100.9"),
		Volume:        1000,
		TS:            ts,
		Complete:      complete,
	}
}

func TestCandleRepo_UpsertAndRange(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := CandleRepo{}

	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	for n := 0; n < 5; n++ {
		c := testCandle(i.UID, base.Add(time.Duration(n)*5*time.Minute), true)
		if err := repo.Upsert(ctx, db, c); err != nil {
			t.Fatalf("Upsert candle %d: %v", n, err)
		}
	}

	got, err := repo.Range(ctx, db, i.UID, model.Interval5m, base, base.Add(20*time.Minute))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("len(Range) = %d, want 5", len(got))
	}
	for n := 1; n < len(got); n++ {
		if !got[n].TS.After(got[n-1].TS) {
			t.Errorf("Range not ascending by ts at index %d", n)
		}
	}
	if got[0].Open.String() != "100.5" || got[0].Close.String() != "100.9" {
		t.Errorf("decimal round trip: Open=%s Close=%s, want 100.5/100.9", got[0].Open, got[0].Close)
	}
	if !got[0].TS.Equal(base) || got[0].TS.Location() != time.UTC {
		t.Errorf("TS round trip = %v, want %v in UTC", got[0].TS, base)
	}
}

func TestCandleRepo_UpsertOnConflictOverwrites(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := CandleRepo{}
	ts := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	c := testCandle(i.UID, ts, false)
	if err := repo.Upsert(ctx, db, c); err != nil {
		t.Fatalf("Upsert (initial, incomplete): %v", err)
	}

	c.Close = model.MustDecimal("105")
	c.Complete = true
	if err := repo.Upsert(ctx, db, c); err != nil {
		t.Fatalf("Upsert (overwrite, complete): %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM candles WHERE instrument_uid = ? AND interval = ? AND ts = ?`,
		string(i.UID), model.Interval5m.String(), timeText(ts)).Scan(&count); err != nil {
		t.Fatalf("count candles: %v", err)
	}
	if count != 1 {
		t.Fatalf("candle row count = %d, want 1 (unique constraint must upsert, not duplicate)", count)
	}

	got, ok, err := repo.LatestComplete(ctx, db, i.UID, model.Interval5m)
	if err != nil {
		t.Fatalf("LatestComplete: %v", err)
	}
	if !ok {
		t.Fatal("LatestComplete: ok = false, want true after upsert set complete=true")
	}
	if got.Close.String() != "105" {
		t.Errorf("LatestComplete().Close = %s, want 105 (overwritten value)", got.Close)
	}
}

func TestCandleRepo_UpsertDoesNotRegressComplete(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := CandleRepo{}
	ts := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	// Store a completed bar with a final close.
	final := testCandle(i.UID, ts, true)
	final.Close = model.MustDecimal("105")
	if err := repo.Upsert(ctx, db, final); err != nil {
		t.Fatalf("Upsert (complete): %v", err)
	}

	// A late incomplete update for the same timestamp must not overwrite it or
	// flip complete back to 0.
	late := testCandle(i.UID, ts, false)
	late.Close = model.MustDecimal("99")
	if err := repo.Upsert(ctx, db, late); err != nil {
		t.Fatalf("Upsert (late incomplete): %v", err)
	}

	got, ok, err := repo.LatestComplete(ctx, db, i.UID, model.Interval5m)
	if err != nil {
		t.Fatalf("LatestComplete: %v", err)
	}
	if !ok {
		t.Fatal("LatestComplete: ok = false, want true (complete bar must survive)")
	}
	if got.Close.String() != "105" {
		t.Errorf("Close = %s, want 105 (incomplete update must not overwrite the complete bar)", got.Close)
	}
	if !got.Complete {
		t.Error("Complete = false, want true (incomplete update must not regress the watermark)")
	}

	// A complete->complete correction is still allowed.
	corrected := testCandle(i.UID, ts, true)
	corrected.Close = model.MustDecimal("106")
	if err := repo.Upsert(ctx, db, corrected); err != nil {
		t.Fatalf("Upsert (complete correction): %v", err)
	}
	got, _, err = repo.LatestComplete(ctx, db, i.UID, model.Interval5m)
	if err != nil {
		t.Fatalf("LatestComplete after correction: %v", err)
	}
	if got.Close.String() != "106" {
		t.Errorf("Close after complete correction = %s, want 106", got.Close)
	}
}

func TestCandleRepo_LatestComplete_IgnoresIncomplete(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := CandleRepo{}
	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	if err := repo.Upsert(ctx, db, testCandle(i.UID, base, true)); err != nil {
		t.Fatalf("Upsert complete: %v", err)
	}
	if err := repo.Upsert(ctx, db, testCandle(i.UID, base.Add(5*time.Minute), false)); err != nil {
		t.Fatalf("Upsert incomplete: %v", err)
	}

	got, ok, err := repo.LatestComplete(ctx, db, i.UID, model.Interval5m)
	if err != nil {
		t.Fatalf("LatestComplete: %v", err)
	}
	if !ok {
		t.Fatal("LatestComplete: ok = false, want true")
	}
	if !got.TS.Equal(base) {
		t.Errorf("LatestComplete().TS = %v, want %v (must skip the incomplete, later bar)", got.TS, base)
	}
}

func TestCandleRepo_LatestComplete_NoneFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")

	_, ok, err := (CandleRepo{}).LatestComplete(ctx, db, i.UID, model.Interval5m)
	if err != nil {
		t.Fatalf("LatestComplete: %v", err)
	}
	if ok {
		t.Error("LatestComplete: ok = true, want false with no candles")
	}
}

// TestCandleRepo_LatestCompleteAsOf_Boundary proves the at-or-before-as_of
// contract the cycle orchestrator's assemble step depends on: a bar exactly
// at as_of is included (not excluded by a strict "<"), and a later bar must
// not leak into the read. Note: candles carry a UNIQUE(instrument_uid,
// interval, ts) constraint, so — unlike quotes/feature_snapshots — two candle
// rows can never share a ts; there is no id-tie-break case to test here.
func TestCandleRepo_LatestCompleteAsOf_Boundary(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := CandleRepo{}
	asOf := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	atAsOf := testCandle(i.UID, asOf, true)
	atAsOf.Close = model.MustDecimal("100")
	if err := repo.Upsert(ctx, db, atAsOf); err != nil {
		t.Fatalf("Upsert at as_of: %v", err)
	}
	after := testCandle(i.UID, asOf.Add(5*time.Minute), true)
	after.Close = model.MustDecimal("200")
	if err := repo.Upsert(ctx, db, after); err != nil {
		t.Fatalf("Upsert after as_of: %v", err)
	}

	got, ok, err := repo.LatestCompleteAsOf(ctx, db, i.UID, model.Interval5m, asOf)
	if err != nil {
		t.Fatalf("LatestCompleteAsOf: %v", err)
	}
	if !ok {
		t.Fatal("LatestCompleteAsOf: ok = false, want true")
	}
	if !got.TS.Equal(asOf) {
		t.Errorf("LatestCompleteAsOf(asOf).TS = %v, want %v (a bar exactly at as_of must be included)", got.TS, asOf)
	}
	if got.Close.String() != "100" {
		t.Errorf("LatestCompleteAsOf(asOf).Close = %s, want 100 (a bar after as_of must not leak)", got.Close)
	}
}

// TestCandleRepo_LatestCompleteAsOf_IgnoresIncomplete proves the complete=1
// filter still applies under an as-of read: an incomplete bar sitting right
// at the boundary must not be returned in place of an earlier complete one.
func TestCandleRepo_LatestCompleteAsOf_IgnoresIncomplete(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := CandleRepo{}
	asOf := time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC)
	earlier := asOf.Add(-5 * time.Minute)

	if err := repo.Upsert(ctx, db, testCandle(i.UID, earlier, true)); err != nil {
		t.Fatalf("Upsert complete: %v", err)
	}
	if err := repo.Upsert(ctx, db, testCandle(i.UID, asOf, false)); err != nil {
		t.Fatalf("Upsert incomplete at as_of: %v", err)
	}

	got, ok, err := repo.LatestCompleteAsOf(ctx, db, i.UID, model.Interval5m, asOf)
	if err != nil {
		t.Fatalf("LatestCompleteAsOf: %v", err)
	}
	if !ok {
		t.Fatal("LatestCompleteAsOf: ok = false, want true")
	}
	if !got.TS.Equal(earlier) {
		t.Errorf("LatestCompleteAsOf().TS = %v, want %v (the incomplete bar at as_of must not count)", got.TS, earlier)
	}
}

func TestCandleRepo_LatestCompleteAsOf_BeforeAnyData(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := CandleRepo{}
	ts := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	if err := repo.Upsert(ctx, db, testCandle(i.UID, ts, true)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	_, ok, err := repo.LatestCompleteAsOf(ctx, db, i.UID, model.Interval5m, ts.Add(-time.Minute))
	if err != nil {
		t.Fatalf("LatestCompleteAsOf: %v", err)
	}
	if ok {
		t.Error("LatestCompleteAsOf(before any data): ok = true, want false")
	}
}

func TestCandleRepo_LatestCompleteAsOf_NoneFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")

	_, ok, err := (CandleRepo{}).LatestCompleteAsOf(ctx, db, i.UID, model.Interval5m, time.Now())
	if err != nil {
		t.Fatalf("LatestCompleteAsOf: %v", err)
	}
	if ok {
		t.Error("LatestCompleteAsOf: ok = true, want false with no candles")
	}
}
