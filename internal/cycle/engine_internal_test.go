package cycle

import (
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestNextBoundaryCoalesces(t *testing.T) {
	d := 5 * time.Minute
	cases := []struct {
		now, want string
	}{
		{"2026-07-20T12:00:00Z", "2026-07-20T12:05:00Z"}, // exactly on a boundary → next
		{"2026-07-20T12:03:00Z", "2026-07-20T12:05:00Z"}, // mid-interval → next
		{"2026-07-20T12:12:00Z", "2026-07-20T12:15:00Z"}, // overran two boundaries → coalesces to the next
	}
	for _, c := range cases {
		now := mustTime(t, c.now)
		got := nextBoundary(now, d)
		if !got.Equal(mustTime(t, c.want)) {
			t.Errorf("nextBoundary(%s) = %s, want %s", c.now, got.Format(time.RFC3339), c.want)
		}
		if !got.After(now) {
			t.Errorf("nextBoundary(%s) = %s is not strictly after now", c.now, got.Format(time.RFC3339))
		}
	}
}

func TestIntervalDuration(t *testing.T) {
	cases := map[model.CandleInterval]time.Duration{
		model.Interval1m: time.Minute, model.Interval5m: 5 * time.Minute,
		model.Interval15m: 15 * time.Minute, model.Interval1h: time.Hour, model.Interval1d: 24 * time.Hour,
	}
	for iv, want := range cases {
		if got := intervalDuration(iv); got != want {
			t.Errorf("intervalDuration(%s) = %s, want %s", iv, got, want)
		}
	}
}

func TestSessionWindowGate(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{cfg: Config{SessionStart: "10:00", SessionEnd: "18:45", Location: loc}}

	// Moscow is UTC+3: 10:00–18:45 MSK is 07:00–15:45 UTC on the same date.
	now := mustTime(t, "2026-07-20T12:00:00Z")
	sess := e.sessionWindow(now)
	if !sess.Start.Equal(mustTime(t, "2026-07-20T07:00:00Z")) || !sess.End.Equal(mustTime(t, "2026-07-20T15:45:00Z")) {
		t.Fatalf("session = [%s, %s], want [07:00Z, 15:45Z]", sess.Start.Format(time.RFC3339), sess.End.Format(time.RFC3339))
	}
	if !sess.IsOpen(now) {
		t.Error("12:00Z should be inside the session")
	}
	if sess.IsOpen(mustTime(t, "2026-07-20T16:00:00Z")) {
		t.Error("16:00Z should be outside the session")
	}
	if sess.IsOpen(mustTime(t, "2026-07-20T06:00:00Z")) {
		t.Error("06:00Z should be before the session")
	}

	// No configured hours → 24h open.
	open := (&Engine{cfg: Config{}}).sessionWindow(now)
	if !open.IsOpen(mustTime(t, "2026-07-20T03:00:00Z")) {
		t.Error("unconfigured session must be 24h open")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt.UTC()
}
