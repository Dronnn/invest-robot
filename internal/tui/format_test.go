package tui

import (
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestGroupThousands(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"0":       "0",
		"12":      "12",
		"123":     "123",
		"1234":    "1,234",
		"12345":   "12,345",
		"123456":  "123,456",
		"1234567": "1,234,567",
	}
	for in, want := range cases {
		if got := groupThousands(in); got != want {
			t.Errorf("groupThousands(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDecimal(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"0", "0"},
		{"1", "1"},
		{"999", "999"},
		{"1000", "1,000"},
		{"1234567.89", "1,234,567.89"},
		{"-1000", "-1,000"},
		{"-1234567.5", "-1,234,567.5"},
		{"0.5", "0.5"},
		{"1000000", "1,000,000"},
		{"-0.001", "-0.001"},
	}
	for _, c := range cases {
		got := formatDecimal(model.MustDecimal(c.in))
		if got != c.want {
			t.Errorf("formatDecimal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatSigned(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"0", "0"},
		{"1500.5", "+1,500.5"},
		{"-1500.5", "-1,500.5"},
		{"1000000", "+1,000,000"},
	}
	for _, c := range cases {
		got := formatSigned(model.MustDecimal(c.in))
		if got != c.want {
			t.Errorf("formatSigned(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	if got := formatMoney(model.MustDecimal("1234.5"), "rub"); got != "1,234.5 RUB" {
		t.Errorf("formatMoney = %q", got)
	}
	if got := formatMoney(model.MustDecimal("1234.5"), ""); got != "1,234.5" {
		t.Errorf("formatMoney no currency = %q", got)
	}
}

func TestFormatQty(t *testing.T) {
	cases := map[int64]string{
		0:      "0",
		5:      "5",
		1500:   "1,500",
		-1500:  "-1,500",
		123456: "123,456",
	}
	for in, want := range cases {
		if got := formatQty(in); got != want {
			t.Errorf("formatQty(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCountdown(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if got := countdown(now, time.Time{}); got != dash {
		t.Errorf("countdown(zero) = %q, want %q", got, dash)
	}
	if got := countdown(now, now.Add(-time.Second)); got != "now" {
		t.Errorf("countdown(past) = %q, want now", got)
	}
	if got := countdown(now, now.Add(90*time.Second)); got != "1m30s" {
		t.Errorf("countdown(90s) = %q, want 1m30s", got)
	}
	if got := countdown(now, now.Add(45*time.Second)); got != "45s" {
		t.Errorf("countdown(45s) = %q, want 45s", got)
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if got := ago(now, time.Time{}); got != dash {
		t.Errorf("ago(zero) = %q, want %q", got, dash)
	}
	if got := ago(now, now.Add(-10*time.Second)); got != "10s ago" {
		t.Errorf("ago(10s) = %q, want 10s ago", got)
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:           "30s",
		90 * time.Second:           "1m30s",
		2 * time.Minute:            "2m",
		time.Hour + 30*time.Minute: "1h30m",
		2 * time.Hour:              "2h",
	}
	for in, want := range cases {
		if got := shortDuration(in); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
		{"日本語テスト", 3, "日本…"},
	}
	for _, c := range cases {
		if got := truncate(c.s, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"":         ModePaper,
		"paper":    ModePaper,
		"PAPER":    ModePaper,
		"backtest": ModeBacktest,
		"real":     ModeReal,
		"weird":    "WEIRD",
	}
	for in, want := range cases {
		if got := normalizeMode(in); got != want {
			t.Errorf("normalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}
