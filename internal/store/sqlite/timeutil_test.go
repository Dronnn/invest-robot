package sqlite

import (
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

func TestTimeText_NonUTCInputNormalizes(t *testing.T) {
	loc := time.FixedZone("MSK", 3*60*60)
	moscow := time.Date(2026, 7, 19, 15, 0, 0, 0, loc)
	text := timeText(moscow)
	if text != "2026-07-19T12:00:00Z" {
		t.Errorf("timeText(moscow 15:00 MSK) = %q, want 2026-07-19T12:00:00Z", text)
	}
}
