package sqlite

import (
	"context"
	"errors"
	"testing"
)

func TestCycleRepo_InsertGetUpdateStatus(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := CycleRepo{}

	id := seedCycle(t, db)
	if id == 0 {
		t.Fatal("Insert returned id = 0")
	}

	got, err := repo.Get(ctx, db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Mode != "paper" || got.Engine != "rules" || got.Status != "running" {
		t.Errorf("Get() = %+v, want mode=paper engine=rules status=running", got)
	}

	if err := repo.UpdateStatus(ctx, db, id, "completed"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err = repo.Get(ctx, db, id)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("Status after UpdateStatus = %q, want completed", got.Status)
	}
}

func TestCycleRepo_GetNotFound(t *testing.T) {
	db := openTest(t)
	_, err := (CycleRepo{}).Get(context.Background(), db, 999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(999) error = %v, want ErrNotFound", err)
	}
}

func TestCycleRepo_UpdateStatusNotFound(t *testing.T) {
	db := openTest(t)
	err := (CycleRepo{}).UpdateStatus(context.Background(), db, 999, "completed")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateStatus(999) error = %v, want ErrNotFound", err)
	}
}

func TestCycleRepo_Recent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := CycleRepo{}

	var ids []int64
	for n := 0; n < 3; n++ {
		ids = append(ids, seedCycle(t, db))
	}

	recent, err := repo.Recent(ctx, db, 2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("len(Recent(2)) = %d, want 2", len(recent))
	}
}
