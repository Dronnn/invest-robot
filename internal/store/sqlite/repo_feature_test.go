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

// TestFeatureSnapshotRepo_LatestAsOf_Boundary proves the at-or-before-as_of
// contract the cycle orchestrator's assemble step depends on: a snapshot
// exactly at as_of is included (not excluded by a strict "<"), and a later
// snapshot must not leak into the read.
func TestFeatureSnapshotRepo_LatestAsOf_Boundary(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := FeatureSnapshotRepo{}
	asOf := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	if _, err := repo.Insert(ctx, db, FeatureSnapshot{InstrumentUID: i.UID, AsOf: asOf, Payload: `{"sma20":100}`}); err != nil {
		t.Fatalf("Insert at as_of: %v", err)
	}
	if _, err := repo.Insert(ctx, db, FeatureSnapshot{InstrumentUID: i.UID, AsOf: asOf.Add(5 * time.Minute), Payload: `{"sma20":200}`}); err != nil {
		t.Fatalf("Insert after as_of: %v", err)
	}

	got, ok, err := repo.LatestAsOf(ctx, db, i.UID, asOf)
	if err != nil {
		t.Fatalf("LatestAsOf: %v", err)
	}
	if !ok {
		t.Fatal("LatestAsOf: ok = false, want true")
	}
	if !got.AsOf.Equal(asOf) {
		t.Errorf("LatestAsOf(asOf).AsOf = %v, want %v (a snapshot exactly at as_of must be included)", got.AsOf, asOf)
	}
	if got.Payload != `{"sma20":100}` {
		t.Errorf("LatestAsOf(asOf).Payload = %s, want the boundary snapshot (a later snapshot must not leak)", got.Payload)
	}
}

// TestFeatureSnapshotRepo_LatestAsOf_TieBreaksByID proves the id DESC
// tie-breaker makes the as-of read deterministic when two snapshots share an
// identical as_of: the most recently inserted (highest id) wins.
func TestFeatureSnapshotRepo_LatestAsOf_TieBreaksByID(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")
	repo := FeatureSnapshotRepo{}
	asOf := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	id1, err := repo.Insert(ctx, db, FeatureSnapshot{InstrumentUID: i.UID, AsOf: asOf, Payload: `{"sma20":100}`})
	if err != nil {
		t.Fatalf("Insert first: %v", err)
	}
	id2, err := repo.Insert(ctx, db, FeatureSnapshot{InstrumentUID: i.UID, AsOf: asOf, Payload: `{"sma20":101}`})
	if err != nil {
		t.Fatalf("Insert second: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("Insert returned the same id twice: %d", id1)
	}

	got, ok, err := repo.LatestAsOf(ctx, db, i.UID, asOf)
	if err != nil || !ok {
		t.Fatalf("LatestAsOf: ok=%v err=%v", ok, err)
	}
	if got.Payload != `{"sma20":101}` {
		t.Errorf("LatestAsOf(asOf).Payload = %s, want the highest-id row on an equal-as_of tie", got.Payload)
	}
}

func TestFeatureSnapshotRepo_LatestAsOf_NoneFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	i := seedInstrument(t, db, "uid-1")

	_, ok, err := (FeatureSnapshotRepo{}).LatestAsOf(ctx, db, i.UID, time.Now())
	if err != nil {
		t.Fatalf("LatestAsOf: %v", err)
	}
	if ok {
		t.Error("LatestAsOf: ok = true, want false with no snapshots")
	}
}
