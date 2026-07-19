package sqlite

import (
	"sort"
	"testing"
	"time"
)

func TestTimeText_RoundTrip(t *testing.T) {
	loc := time.FixedZone("MSK", 3*60*60)
	cases := []time.Time{
		time.Date(2026, 7, 19, 12, 30, 0, 0, time.UTC),
		time.Date(2026, 7, 19, 12, 30, 0, 123456789, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, loc), // non-UTC input must normalize
		time.Unix(0, 0).UTC(),
	}
	for _, want := range cases {
		text := timeText(want)
		got, err := parseTimeText(text)
		if err != nil {
			t.Fatalf("parseTimeText(%q): %v", text, err)
		}
		if !got.Equal(want) {
			t.Errorf("round trip %v -> %q -> %v: not equal", want, text, got)
		}
		if got.Location() != time.UTC {
			t.Errorf("round trip %v -> %q -> %v: location = %v, want UTC", want, text, got, got.Location())
		}
	}
}

func TestTimeText_FixedWidth(t *testing.T) {
	moscow := time.Date(2026, 7, 19, 15, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	if got := timeText(moscow); got != "2026-07-19T12:00:00.000000000Z" {
		t.Errorf("timeText(moscow 15:00 MSK) = %q, want 2026-07-19T12:00:00.000000000Z", got)
	}
	// A whole-second value renders with a full nine-digit zero fraction, not a
	// bare "…Z" — this is what keeps string order aligned with time order.
	whole := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if got := timeText(whole); len(got) != len("2026-07-19T12:00:00.000000000Z") {
		t.Errorf("timeText produced variable width: %q", got)
	}
}

// TestTimeText_LexicalOrderMatchesChronological is the regression for the
// blocker: the encoding must sort lexically in the same order it sorts
// chronologically, including across the whole-second/subsecond boundary that
// broke RFC3339Nano ("…00.000000001Z" must sort after "…00Z", not before).
func TestTimeText_LexicalOrderMatchesChronological(t *testing.T) {
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	times := []time.Time{
		base,
		base.Add(time.Nanosecond),
		base.Add(time.Microsecond),
		base.Add(500 * time.Millisecond),
		base.Add(time.Second),
		base.Add(time.Second + time.Nanosecond),
		base.Add(time.Minute),
	}
	// Shuffle-independent: sort the encoded strings and confirm the decoded
	// instants come out in ascending chronological order.
	encoded := make([]string, len(times))
	for i, tm := range times {
		encoded[i] = timeText(tm)
	}
	sort.Strings(encoded)
	for i := 1; i < len(encoded); i++ {
		prev, err := parseTimeText(encoded[i-1])
		if err != nil {
			t.Fatalf("parse %q: %v", encoded[i-1], err)
		}
		cur, err := parseTimeText(encoded[i])
		if err != nil {
			t.Fatalf("parse %q: %v", encoded[i], err)
		}
		if !cur.After(prev) {
			t.Errorf("lexical order disagrees with time order: %q (%v) not after %q (%v)", encoded[i], cur, encoded[i-1], prev)
		}
	}
}
