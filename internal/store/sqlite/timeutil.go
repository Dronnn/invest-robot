package sqlite

import "time"

// timeLayout is the fixed-width UTC layout every TEXT timestamp column uses
// (DESIGN §5). Unlike time.RFC3339Nano, the fractional-second field is always
// exactly nine digits, so two stored timestamps compare identically as strings
// and as instants — the ordering contract every "latest" and range query
// depends on. RFC3339Nano trims trailing zero fractions, which makes
// "…00.000000001Z" sort before the earlier "…00Z"; that variable width is the
// storage-ordering bug this replaces. The trailing Z is a literal, not a zone
// token: timeText always normalizes to UTC before formatting, so the zone is
// invariably Z and parseTimeText reads it back as UTC.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// timeText renders t as the canonical fixed-width UTC timestamp string.
func timeText(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTimeText parses a stored UTC timestamp, always returning UTC. It reads
// the fixed-width form timeText writes and, for resilience against a
// hand-edited or externally written row, falls back to plain RFC3339Nano.
func parseTimeText(s string) (time.Time, error) {
	if t, err := time.Parse(timeLayout, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
