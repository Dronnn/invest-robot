package sqlite

import (
	"context"
	"testing"
	"time"
)

func TestEventRepo_InsertAndRecent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	repo := EventRepo{}
	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	for n := 0; n < 3; n++ {
		e := Event{TS: base.Add(time.Duration(n) * time.Second), Level: "info", Code: "cycle.started", Payload: `{"n":1}`}
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
	if recent[0].Payload != `{"n":1}` {
		t.Errorf("Payload = %q, want {\"n\":1}", recent[0].Payload)
	}
}

func TestEventRepo_NoPayload(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, err := (EventRepo{}).Insert(ctx, db, Event{TS: time.Now(), Level: "warn", Code: "stream.gap"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	recent, err := (EventRepo{}).Recent(ctx, db, 1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 1 || recent[0].Payload != "" {
		t.Errorf("Recent = %+v, want one event with empty payload", recent)
	}
}
