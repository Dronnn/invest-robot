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
