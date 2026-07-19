package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Trading modes, as they appear in the status-bar badge (DESIGN §9/§10).
const (
	ModePaper    = "PAPER"
	ModeBacktest = "BACKTEST"
	ModeReal     = "REAL"
)

// normalizeMode upper-cases a mode string and maps the empty value to PAPER,
// which is always the default (DESIGN §10). An unrecognised value is returned
// upper-cased as-is so it is at least visible rather than silently hidden.
func normalizeMode(mode string) string {
	m := strings.ToUpper(strings.TrimSpace(mode))
	if m == "" {
		return ModePaper
	}
	return m
}

// groupThousands inserts ',' as a thousands separator into a run of decimal
// digits (the integer part of a number, no sign, no decimal point). Digits
// shorter than four are returned unchanged.
func groupThousands(digits string) string {
	n := len(digits)
	if n <= 3 {
		return digits
	}
	var b strings.Builder
	// The first group is the leading 1-3 digits; every subsequent group is 3.
	first := n % 3
	if first == 0 {
		first = 3
	}
	b.WriteString(digits[:first])
	for i := first; i < n; i += 3 {
		b.WriteByte(',')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// formatDecimal renders d in canonical form with thousands separators on the
// integer part: "1234567.89" -> "1,234,567.89", "-1000" -> "-1,000", "0" ->
// "0". The fractional part (if any) is left untouched.
func formatDecimal(d model.Decimal) string {
	s := d.String()
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, fracPart := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPart = s[:dot]
		fracPart = s[dot:] // includes the '.'
	}
	grouped := groupThousands(intPart) + fracPart
	if neg {
		return "-" + grouped
	}
	return grouped
}

// formatSigned is formatDecimal with an explicit leading '+' for strictly
// positive values (negatives already carry '-', zero stays "0"). Used for
// P&L / deltas where the sign carries meaning.
func formatSigned(d model.Decimal) string {
	s := formatDecimal(d)
	if d.Sign() > 0 {
		return "+" + s
	}
	return s
}

// formatMoney appends a currency label to a grouped decimal, e.g.
// "1,234.50 RUB". An empty currency yields just the number.
func formatMoney(d model.Decimal, currency string) string {
	if currency == "" {
		return formatDecimal(d)
	}
	return formatDecimal(d) + " " + strings.ToUpper(currency)
}

// dash is the placeholder rendered wherever a value is unknown/unavailable so
// the layout stays aligned instead of collapsing.
const dash = "—"

// formatQty renders a lot quantity with thousands separators, keeping the sign.
func formatQty(qty int64) string {
	s := strconv.FormatInt(qty, 10)
	if strings.HasPrefix(s, "-") {
		return "-" + groupThousands(s[1:])
	}
	return groupThousands(s)
}

// clockTime renders a timestamp as local wall-clock HH:MM:SS, or dash for the
// zero time.
func clockTime(t time.Time) string {
	if t.IsZero() {
		return dash
	}
	return t.Local().Format("15:04:05")
}

// dateTime renders a timestamp as "2006-01-02 15:04:05" (local), or dash for
// the zero time.
func dateTime(t time.Time) string {
	if t.IsZero() {
		return dash
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// countdown renders the non-negative duration until t as "1m30s" / "0s", or
// dash if t is the zero time. A target already in the past renders "now".
func countdown(now, t time.Time) string {
	if t.IsZero() {
		return dash
	}
	d := t.Sub(now)
	if d <= 0 {
		return "now"
	}
	return shortDuration(d)
}

// ago renders how long before now t was, as "5s ago" / "3m ago" / dash for the
// zero time. Coarse by design — it feeds a freshness indicator, not a timer.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return dash
	}
	d := now.Sub(t)
	if d < 0 {
		return "now"
	}
	return shortDuration(d) + " ago"
}

// itoa is strconv.FormatInt for base 10, kept short for the duration builders.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// shortDuration renders d rounded to a readable granularity: sub-minute in
// seconds, sub-hour in "MmSs", else "HhMm".
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return itoa(int64(d.Round(time.Second)/time.Second)) + "s"
	}
	if d < time.Hour {
		d = d.Round(time.Second)
		m := int64(d / time.Minute)
		s := int64((d % time.Minute) / time.Second)
		if s == 0 {
			return itoa(m) + "m"
		}
		return itoa(m) + "m" + itoa(s) + "s"
	}
	d = d.Round(time.Minute)
	h := int64(d / time.Hour)
	m := int64((d % time.Hour) / time.Minute)
	if m == 0 {
		return itoa(h) + "h"
	}
	return itoa(h) + "h" + itoa(m) + "m"
}

// truncate shortens s to at most n runes, appending an ellipsis when cut. It is
// rune-aware so multi-byte content is not split mid-character.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
