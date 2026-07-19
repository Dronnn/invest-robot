package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestEquityRepo_InsertLatestRange(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := EquityRepo{}
	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	for n := 0; n < 3; n++ {
		s := EquitySnapshot{
			TS:          base.Add(time.Duration(n) * time.Hour),
			Cash:        model.MustDecimal("1000"),
			MarketValue: model.MustDecimal("500"),
			Total:       model.MustDecimal("1500"),
		}
		if _, err := repo.Insert(ctx, db, s); err != nil {
			t.Fatalf("Insert %d: %v", n, err)
		}
	}

	latest, ok, err := repo.Latest(ctx, db)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !ok {
		t.Fatal("Latest: ok = false, want true")
	}
	if !latest.TS.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("Latest().TS = %v, want %v", latest.TS, base.Add(2*time.Hour))
	}

	got, err := repo.Range(ctx, db, base, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Range) = %d, want 2", len(got))
	}
	if got[0].Total.String() != "1500" {
		t.Errorf("Total = %s, want 1500", got[0].Total)
	}
}

func TestEquityRepo_Latest_NoneFound(t *testing.T) {
	db := openTest(t)
	_, ok, err := (EquityRepo{}).Latest(context.Background(), db)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if ok {
		t.Error("Latest: ok = true, want false with no snapshots")
	}
}
