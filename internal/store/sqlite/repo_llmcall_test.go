package sqlite

import (
	"context"
	"testing"
)

func TestLLMCallRepo_InsertAndListByCycle(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	cycleID := seedCycle(t, db)
	repo := LLMCallRepo{}

	c := LLMCall{
		CycleID:    cycleID,
		Model:      "claude-cli",
		Request:    `{"prompt":"..."}`,
		Response:   `{"actions":[]}`,
		DurationMS: 1234,
		CreatedAt:  nowUTC(),
	}
	id, err := repo.Insert(ctx, db, c)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	list, err := repo.ListByCycle(ctx, db, cycleID)
	if err != nil {
		t.Fatalf("ListByCycle: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	got := list[0]
	if got.ID != id || got.Model != "claude-cli" || got.Request != c.Request || got.Response != c.Response || got.DurationMS != 1234 {
		t.Errorf("ListByCycle()[0] = %+v, want matching %+v", got, c)
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty", got.Error)
	}
	if !got.CreatedAt.Equal(c.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, c.CreatedAt)
	}
}

func TestLLMCallRepo_WithError(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	cycleID := seedCycle(t, db)

	c := LLMCall{
		CycleID:    cycleID,
		Model:      "claude-cli",
		Request:    `{}`,
		DurationMS: 5000,
		Error:      "timeout",
		CreatedAt:  nowUTC(),
	}
	if _, err := (LLMCallRepo{}).Insert(ctx, db, c); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	list, err := (LLMCallRepo{}).ListByCycle(ctx, db, cycleID)
	if err != nil {
		t.Fatalf("ListByCycle: %v", err)
	}
	if len(list) != 1 || list[0].Error != "timeout" {
		t.Errorf("list = %+v, want one call with Error=timeout", list)
	}
	if list[0].Response != "" {
		t.Errorf("Response = %q, want empty when the call errored", list[0].Response)
	}
}
