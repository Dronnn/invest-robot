package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestInstrumentRepo_UpsertAndGet(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := InstrumentRepo{}

	i := testInstrument("uid-1")
	if err := repo.Upsert(ctx, db, i, nowUTC()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx, db, "uid-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UID != i.UID || got.FIGI != i.FIGI || got.Ticker != i.Ticker || got.ClassCode != i.ClassCode {
		t.Errorf("Get() ref = %+v, want %+v", got.InstrumentRef, i.InstrumentRef)
	}
	if got.Lot != i.Lot || got.Currency != i.Currency || got.Name != i.Name {
		t.Errorf("Get() = %+v, want %+v", got, i)
	}
	if got.MinPriceIncrement.String() != i.MinPriceIncrement.String() {
		t.Errorf("MinPriceIncrement = %s, want %s (decimal TEXT round-trip)", got.MinPriceIncrement, i.MinPriceIncrement)
	}
}

func TestInstrumentRepo_UpsertReplacesFields(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := InstrumentRepo{}

	i := testInstrument("uid-1")
	if err := repo.Upsert(ctx, db, i, nowUTC()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	i.Name = "Renamed"
	i.Lot = 20
	if err := repo.Upsert(ctx, db, i, nowUTC()); err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}

	got, err := repo.Get(ctx, db, "uid-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Renamed" || got.Lot != 20 {
		t.Errorf("Get() after upsert = %+v, want Name=Renamed Lot=20", got)
	}

	list, err := repo.List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len(list) = %d, want 1 (upsert must not create a duplicate row)", len(list))
	}
}

func TestInstrumentRepo_GetNotFound(t *testing.T) {
	db := openTest(t)
	_, err := (InstrumentRepo{}).Get(context.Background(), db, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func TestInstrumentRepo_List(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := InstrumentRepo{}
	seedInstrument(t, db, "uid-b")
	seedInstrument(t, db, "uid-a")

	list, err := repo.List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].UID != "uid-a" || list[1].UID != "uid-b" {
		t.Errorf("List() not ordered by uid: %v, %v", list[0].UID, list[1].UID)
	}
}

func TestInstrumentRepo_UpsertInsideTx(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	err := WithTx(ctx, db.DB, func(ctx context.Context, tx *sql.Tx) error {
		return (InstrumentRepo{}).Upsert(ctx, tx, testInstrument("uid-tx"), nowUTC())
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	_, err = (InstrumentRepo{}).Get(ctx, db, "uid-tx")
	if err != nil {
		t.Fatalf("Get after tx commit: %v", err)
	}
}
