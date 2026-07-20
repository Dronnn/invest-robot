package sqlite

import (
	"context"
	"testing"
	"time"
)

func TestExecSessionRepo_UpsertAndGet(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	if _, found, err := (ExecSessionRepo{}).Get(ctx, db); err != nil || found {
		t.Fatalf("empty Get = (found=%v, err=%v), want (false, nil)", found, err)
	}

	start := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(8 * time.Hour)
	if err := (ExecSessionRepo{}).Upsert(ctx, db, ExecSession{Start: start, End: end}, nowUTC()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, found, err := (ExecSessionRepo{}).Get(ctx, db)
	if err != nil || !found {
		t.Fatalf("Get = (found=%v, err=%v), want (true, nil)", found, err)
	}
	if !got.Start.Equal(start) || !got.End.Equal(end) {
		t.Errorf("session = %v..%v, want %v..%v", got.Start, got.End, start, end)
	}

	// Upsert replaces in place (single row).
	newStart := start.Add(24 * time.Hour)
	if err := (ExecSessionRepo{}).Upsert(ctx, db, ExecSession{Start: newStart, End: newStart.Add(time.Hour)}, nowUTC()); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, _, err = (ExecSessionRepo{}).Get(ctx, db)
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	if !got.Start.Equal(newStart) {
		t.Errorf("session start = %v, want %v (replaced)", got.Start, newStart)
	}
}

func TestHaltRepo_EngageClearStatus(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	if h, err := (HaltRepo{}).Status(ctx, db); err != nil || h.Engaged {
		t.Fatalf("initial status = (%+v, %v), want not engaged", h, err)
	}

	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if err := (HaltRepo{}).Engage(ctx, db, "cash below floor", at); err != nil {
		t.Fatalf("engage: %v", err)
	}
	h, err := (HaltRepo{}).Status(ctx, db)
	if err != nil || !h.Engaged || h.Reason != "cash below floor" || !h.EngagedAt.Equal(at) {
		t.Fatalf("status = (%+v, %v), want engaged with reason and timestamp", h, err)
	}

	// First-writer-wins: a second engage keeps the original reason.
	if err := (HaltRepo{}).Engage(ctx, db, "some other reason", at.Add(time.Hour)); err != nil {
		t.Fatalf("second engage: %v", err)
	}
	if h, _ := (HaltRepo{}).Status(ctx, db); h.Reason != "cash below floor" {
		t.Errorf("reason after second engage = %q, want the original preserved", h.Reason)
	}

	if err := (HaltRepo{}).Clear(ctx, db); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if h, err := (HaltRepo{}).Status(ctx, db); err != nil || h.Engaged {
		t.Fatalf("status after clear = (%+v, %v), want not engaged", h, err)
	}
}

func TestTradingStatusRepo_UpsertAndGet(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	uid := seedInstrument(t, db, "uid-1").UID

	if _, found, err := (TradingStatusRepo{}).Get(ctx, db, uid); err != nil || found {
		t.Fatalf("empty Get = (found=%v, err=%v), want (false, nil)", found, err)
	}

	if err := (TradingStatusRepo{}).Upsert(ctx, db, TradingStatus{
		InstrumentUID: uid, Status: "normal_trading", BuyAvailable: true, SellAvailable: false,
	}, nowUTC()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, found, err := (TradingStatusRepo{}).Get(ctx, db, uid)
	if err != nil || !found {
		t.Fatalf("Get = (found=%v, err=%v), want (true, nil)", found, err)
	}
	if got.Status != "normal_trading" || !got.BuyAvailable || got.SellAvailable {
		t.Errorf("status = %+v, want normal_trading buy=true sell=false", got)
	}

	// Upsert replaces the permissions.
	if err := (TradingStatusRepo{}).Upsert(ctx, db, TradingStatus{
		InstrumentUID: uid, Status: "not_available_for_trading", BuyAvailable: false, SellAvailable: false,
	}, nowUTC()); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, _, err = (TradingStatusRepo{}).Get(ctx, db, uid)
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	if got.Status != "not_available_for_trading" || got.BuyAvailable {
		t.Errorf("status = %+v, want not_available_for_trading buy=false", got)
	}
}
