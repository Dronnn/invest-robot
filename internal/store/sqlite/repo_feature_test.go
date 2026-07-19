package sqlite

import (
	"context"
	"testing"
	"time"
)

func TestFeatureSnapshotRepo_InsertAndLatest(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := FeatureSnapshotRepo{}

	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	id1, err := repo.Insert(ctx, db, FeatureSnapshot{InstrumentUID: i.UID, AsOf: base, Payload: `{"sma20":100}`})
	if err != nil {
		t.Fatalf("Insert 1: %v", err)
	}
	id2, err := repo.Insert(ctx, db, FeatureSnapshot{InstrumentUID: i.UID, AsOf: base.Add(5 * time.Minute), Payload: `{"sma20":101}`})
	if err != nil {
		t.Fatalf("Insert 2: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("Insert returned the same id twice: %d", id1)
	}

	got, ok, err := repo.Latest(ctx, db, i.UID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !ok {
		t.Fatal("Latest: ok = false, want true")
	}
	if got.ID != id2 || got.Payload != `{"sma20":101}` {
		t.Errorf("Latest() = %+v, want id=%d payload of the newest snapshot", got, id2)
	}
	if !got.AsOf.Equal(base.Add(5 * time.Minute)) {
		t.Errorf("Latest().AsOf = %v, want %v", got.AsOf, base.Add(5*time.Minute))
	}
}

func TestFeatureSnapshotRepo_Latest_NoneFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")

	_, ok, err := (FeatureSnapshotRepo{}).Latest(ctx, db, i.UID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if ok {
		t.Error("Latest: ok = true, want false with no snapshots")
	}
}
