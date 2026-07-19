package sqlite

import "time"

// timeText renders t as the canonical UTC RFC3339 TEXT form every timestamp
// column uses (DESIGN §5). RFC3339Nano is used (rather than plain RFC3339) so
// sub-second precision survives a round trip through storage; a value with no
// fractional seconds renders identically under either.
func timeText(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseTimeText parses a stored UTC RFC3339 timestamp.
func parseTimeText(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
