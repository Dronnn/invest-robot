package execution

import (
	"testing"
	"time"
)

func TestSession_IsOpen(t *testing.T) {
	start := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	s := Session{Start: start, End: end}

	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before open", start.Add(-time.Second), false},
		{"exactly at open", start, true},
		{"midday", start.Add(4 * time.Hour), true},
		{"one before close", end.Add(-time.Second), true},
		{"exactly at close is out", end, false},
		{"after close", end.Add(time.Second), false},
	}
	for _, c := range cases {
		if got := s.IsOpen(c.at); got != c.want {
			t.Errorf("%s: IsOpen(%s) = %v, want %v", c.name, c.at, got, c.want)
		}
	}
}

func TestSession_ZeroValueAlwaysOpen(t *testing.T) {
	var s Session // no session restriction => 24h
	for _, at := range []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 19, 3, 30, 0, 0, time.UTC),
		time.Now().UTC(),
	} {
		if !s.IsOpen(at) {
			t.Errorf("zero-value session should be open at %s", at)
		}
	}
}
