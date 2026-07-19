package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestCashRepo_InsertAndBalance(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := CashRepo{}

	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	entries := []CashEntry{
		{TS: base, Delta: model.MustDecimal("100000"), Currency: "rub", Reason: "deposit"},
		{TS: base.Add(time.Minute), Delta: model.MustDecimal("-1000.5"), Currency: "rub", Reason: "fill", Ref: "fill-1"},
		{TS: base.Add(2 * time.Minute), Delta: model.MustDecimal("-5"), Currency: "rub", Reason: "fee", Ref: "fill-1"},
		{TS: base, Delta: model.MustDecimal("500"), Currency: "usd", Reason: "deposit"},
	}
	for n, e := range entries {
		if _, err := repo.Insert(ctx, db, e); err != nil {
			t.Fatalf("Insert entry %d: %v", n, err)
		}
	}

	rub, err := repo.Balance(ctx, db, "rub")
	if err != nil {
		t.Fatalf("Balance(rub): %v", err)
	}
	if rub.String() != "98994.5" {
		t.Errorf("Balance(rub) = %s, want 98994.5", rub)
	}

	usd, err := repo.Balance(ctx, db, "usd")
	if err != nil {
		t.Fatalf("Balance(usd): %v", err)
	}
	if usd.String() != "500" {
		t.Errorf("Balance(usd) = %s, want 500", usd)
	}

	eur, err := repo.Balance(ctx, db, "eur")
	if err != nil {
		t.Fatalf("Balance(eur): %v", err)
	}
	if !eur.IsZero() {
		t.Errorf("Balance(eur) = %s, want 0 (no entries)", eur)
	}
}

func TestCashRepo_Recent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := CashRepo{}
	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	for n := 0; n < 3; n++ {
		e := CashEntry{TS: base.Add(time.Duration(n) * time.Minute), Delta: model.MustDecimal("1"), Currency: "rub", Reason: "deposit"}
		if _, err := repo.Insert(ctx, db, e); err != nil {
			t.Fatalf("Insert %d: %v", n, err)
		}
	}

	recent, err := repo.Recent(ctx, db, 2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("len(Recent(2)) = %d, want 2", len(recent))
	}
	if !recent[0].TS.After(recent[1].TS) {
		t.Errorf("Recent not most-recent-first: %v, %v", recent[0].TS, recent[1].TS)
	}
}
