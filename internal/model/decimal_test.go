package model

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseDecimal_CanonicalRoundTrip(t *testing.T) {
	// Inputs already in canonical form must round-trip byte-identically.
	canonical := []string{
		"0",
		"1",
		"-1",
		"0.5",
		"-0.5",
		"100.017",
		"123.456789",
		"9223372036.854775807",
		"-9223372036.854775807",
		"0.000000001",
		"-0.000000001",
		"9223372036",
	}
	for _, s := range canonical {
		d, err := ParseDecimal(s)
		if err != nil {
			t.Errorf("ParseDecimal(%q) error: %v", s, err)
			continue
		}
		if got := d.String(); got != s {
			t.Errorf("ParseDecimal(%q).String() = %q, want round-trip", s, got)
		}
	}
}

func TestParseDecimal_Canonicalization(t *testing.T) {
	// Inputs that parse but normalize to a different canonical string.
	cases := []struct{ in, want string }{
		{"007", "7"},
		{"1.50", "1.5"},
		{"+1", "1"},
		{"-0", "0"},
		{"0.100", "0.1"},
		{"1.000000000", "1"},
		{"00.5", "0.5"},
		{"+0.0", "0"},
		{"0.000000000", "0"},
	}
	for _, c := range cases {
		d, err := ParseDecimal(c.in)
		if err != nil {
			t.Errorf("ParseDecimal(%q) error: %v", c.in, err)
			continue
		}
		if got := d.String(); got != c.want {
			t.Errorf("ParseDecimal(%q).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseDecimal_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"+",
		"-",
		".",
		"5.",
		".5",
		"1.2.3",
		"abc",
		"1e5",
		"1 000",
		" 1",
		"1 ",
		"0x10",
		"--1",
		"1.2345678901",          // 10 fractional digits
		"0.0000000001",          // 10 fractional digits
		"9223372036.854775808",  // one nano over max
		"9223372037",            // integer part over max
		"99999999999",           // far over max
		"-9223372036.854775808", // MinInt64 boundary is rejected
	}
	for _, s := range invalid {
		if d, err := ParseDecimal(s); err == nil {
			t.Errorf("ParseDecimal(%q) = %s, want error", s, d)
		}
	}
}

func TestMustDecimal_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustDecimal(bad) did not panic")
		}
	}()
	_ = MustDecimal("not-a-number")
}

func TestDecimal_SignZeroAbsNeg(t *testing.T) {
	if !MustDecimal("0").IsZero() {
		t.Error("0 not zero")
	}
	if MustDecimal("0.000000001").IsZero() {
		t.Error("tiny value reported zero")
	}
	if got := MustDecimal("-5").Sign(); got != -1 {
		t.Errorf("Sign(-5) = %d, want -1", got)
	}
	if got := MustDecimal("0").Sign(); got != 0 {
		t.Errorf("Sign(0) = %d, want 0", got)
	}
	if got := MustDecimal("5").Sign(); got != 1 {
		t.Errorf("Sign(5) = %d, want 1", got)
	}
	if got := MustDecimal("-5.5").Abs(); got != MustDecimal("5.5") {
		t.Errorf("Abs(-5.5) = %s, want 5.5", got)
	}
	if got := MustDecimal("5.5").Abs(); got != MustDecimal("5.5") {
		t.Errorf("Abs(5.5) = %s, want 5.5", got)
	}
	if got := MustDecimal("5.5").Neg(); got != MustDecimal("-5.5") {
		t.Errorf("Neg(5.5) = %s, want -5.5", got)
	}
	if got := MustDecimal("0").Neg(); got != MustDecimal("0") {
		t.Errorf("Neg(0) = %s, want 0", got)
	}
}

func TestDecimal_Cmp(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1", "2", -1},
		{"2", "1", 1},
		{"1.5", "1.5", 0},
		{"-1", "1", -1},
		{"0", "-0.000000001", 1},
		{"-9223372036.854775807", "9223372036.854775807", -1},
	}
	for _, c := range cases {
		if got := MustDecimal(c.a).Cmp(MustDecimal(c.b)); got != c.want {
			t.Errorf("Cmp(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDecimal_AddSub(t *testing.T) {
	cases := []struct {
		a, b, sum, diff string
	}{
		{"1.5", "2.25", "3.75", "-0.75"},
		{"0", "0", "0", "0"},
		{"-1", "-2", "-3", "1"},
		{"100.017", "0.003", "100.02", "100.014"},
	}
	for _, c := range cases {
		a, b := MustDecimal(c.a), MustDecimal(c.b)
		sum, err := a.Add(b)
		if err != nil || sum != MustDecimal(c.sum) {
			t.Errorf("Add(%s, %s) = %s, %v, want %s", c.a, c.b, sum, err, c.sum)
		}
		diff, err := a.Sub(b)
		if err != nil || diff != MustDecimal(c.diff) {
			t.Errorf("Sub(%s, %s) = %s, %v, want %s", c.a, c.b, diff, err, c.diff)
		}
	}
}

func TestDecimal_AddSubOverflow(t *testing.T) {
	max := MustDecimal("9223372036.854775807")
	min := MustDecimal("-9223372036.854775807")
	tiny := MustDecimal("0.000000001")

	if _, err := max.Add(tiny); !errors.Is(err, ErrOverflow) {
		t.Errorf("max.Add(tiny) err = %v, want ErrOverflow", err)
	}
	if _, err := min.Sub(tiny); !errors.Is(err, ErrOverflow) {
		t.Errorf("min.Sub(tiny) err = %v, want ErrOverflow", err)
	}
	// Landing exactly on the MinInt64 mantissa must also be rejected.
	if _, err := min.Add(tiny.Neg()); !errors.Is(err, ErrOverflow) {
		t.Errorf("min.Add(-tiny) err = %v, want ErrOverflow", err)
	}
	if _, err := min.Sub(tiny); !errors.Is(err, ErrOverflow) {
		t.Errorf("min.Sub(tiny) err = %v, want ErrOverflow", err)
	}
}

func TestDecimal_MulInt(t *testing.T) {
	cases := []struct {
		a    string
		k    int64
		want string
	}{
		{"1.5", 3, "4.5"},
		{"2", 0, "0"},
		{"0", 999, "0"},
		{"-1.25", 4, "-5"},
		{"0.000000001", 1000000000, "1"},
	}
	for _, c := range cases {
		got, err := MustDecimal(c.a).MulInt(c.k)
		if err != nil || got != MustDecimal(c.want) {
			t.Errorf("MulInt(%s, %d) = %s, %v, want %s", c.a, c.k, got, err, c.want)
		}
	}
	if _, err := MustDecimal("9223372036.854775807").MulInt(2); !errors.Is(err, ErrOverflow) {
		t.Errorf("MulInt overflow not detected")
	}
}

func TestDecimal_MulBps(t *testing.T) {
	cases := []struct {
		a    string
		bps  int64
		want string
	}{
		{"100", 50, "0.5"},      // 0.5% of 100
		{"250.5", 10, "0.2505"}, // 0.1% slippage
		{"1000", 10000, "1000"}, // 100%
		{"1000", 0, "0"},        // 0 bps
		{"-100", 50, "-0.5"},    // negative notional
	}
	for _, c := range cases {
		got, err := MustDecimal(c.a).MulBps(c.bps)
		if err != nil || got != MustDecimal(c.want) {
			t.Errorf("MulBps(%s, %d) = %s, %v, want %s", c.a, c.bps, got, err, c.want)
		}
	}
}

func TestDecimal_MulRatRounding(t *testing.T) {
	cases := []struct {
		a        string
		num, den int64
		mode     Rounding
		want     string
	}{
		// Non-tie fractions.
		{"1", 1, 3, RoundHalfAwayFromZero, "0.333333333"},
		{"2", 1, 3, RoundHalfAwayFromZero, "0.666666667"},
		// Exact .5 ties by rounding mode, positive.
		{"0.000000001", 1, 2, RoundHalfAwayFromZero, "0.000000001"},
		{"0.000000001", 1, 2, RoundTowardZero, "0"},
		{"0.000000001", 1, 2, RoundFloor, "0"},
		{"0.000000001", 1, 2, RoundCeil, "0.000000001"},
		// Exact .5 ties, negative.
		{"-0.000000001", 1, 2, RoundHalfAwayFromZero, "-0.000000001"},
		{"-0.000000001", 1, 2, RoundTowardZero, "0"},
		{"-0.000000001", 1, 2, RoundFloor, "-0.000000001"},
		{"-0.000000001", 1, 2, RoundCeil, "0"},
		// Negative denominator normalizes correctly.
		{"1", -1, 3, RoundHalfAwayFromZero, "-0.333333333"},
		// Exact division needs no rounding.
		{"10", 1, 4, RoundHalfAwayFromZero, "2.5"},
	}
	for _, c := range cases {
		got, err := MustDecimal(c.a).MulRat(c.num, c.den, c.mode)
		if err != nil || got != MustDecimal(c.want) {
			t.Errorf("MulRat(%s, %d/%d, %d) = %s, %v, want %s",
				c.a, c.num, c.den, c.mode, got, err, c.want)
		}
	}
}

func TestDecimal_MulRatErrors(t *testing.T) {
	if _, err := MustDecimal("1").MulRat(1, 0, RoundHalfAwayFromZero); err == nil {
		t.Error("MulRat with zero denominator did not error")
	}
	if _, err := MustDecimal("9223372036.854775807").MulRat(2, 1, RoundHalfAwayFromZero); !errors.Is(err, ErrOverflow) {
		t.Errorf("MulRat overflow not detected")
	}
}

func TestDecimal_RoundToIncrement(t *testing.T) {
	tick := MustDecimal("0.01")
	cases := []struct {
		a    string
		dir  Direction
		want string
	}{
		{"100.017", Floor, "100.01"},
		{"100.017", Ceil, "100.02"},
		{"100.017", Nearest, "100.02"},
		{"100.012", Floor, "100.01"},
		{"100.012", Ceil, "100.02"},
		{"100.012", Nearest, "100.01"},
		{"100.015", Nearest, "100.02"}, // tie rounds away from zero
		{"100.02", Floor, "100.02"},    // exact on tick, unchanged
		{"100.02", Ceil, "100.02"},
		{"100.02", Nearest, "100.02"},
		{"-100.017", Floor, "-100.02"},
		{"-100.017", Ceil, "-100.01"},
		{"-100.017", Nearest, "-100.02"},
		{"-100.015", Nearest, "-100.02"}, // tie away from zero, negative
	}
	for _, c := range cases {
		got, err := MustDecimal(c.a).RoundToIncrement(tick, c.dir)
		if err != nil || got != MustDecimal(c.want) {
			t.Errorf("RoundToIncrement(%s, %v, %d) = %s, %v, want %s",
				c.a, tick, c.dir, got, err, c.want)
		}
	}
}

func TestDecimal_RoundToIncrementBadIncrement(t *testing.T) {
	for _, inc := range []string{"0", "-0.01"} {
		if _, err := MustDecimal("1").RoundToIncrement(MustDecimal(inc), Floor); err == nil {
			t.Errorf("RoundToIncrement with increment %s did not error", inc)
		}
	}
}

func TestDecimal_JSONRoundTrip(t *testing.T) {
	type wrapper struct {
		Price Decimal `json:"price"`
	}
	for _, s := range []string{"0", "1.5", "-0.000000001", "9223372036.854775807"} {
		w := wrapper{Price: MustDecimal(s)}
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatalf("Marshal(%s): %v", s, err)
		}
		want := `{"price":"` + s + `"}`
		if string(b) != want {
			t.Errorf("Marshal(%s) = %s, want %s", s, b, want)
		}
		var back wrapper
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", b, err)
		}
		if back.Price != w.Price {
			t.Errorf("round-trip %s = %s", s, back.Price)
		}
	}
}

func TestDecimal_UnmarshalRejectsNonString(t *testing.T) {
	for _, in := range []string{`1.5`, `123`, `null`, `true`, `"abc"`, `"1.2345678901"`} {
		var d Decimal
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("Unmarshal(%s) succeeded, want error (strings-only contract)", in)
		}
	}
}

func TestDecimal_ValuerScanner(t *testing.T) {
	v, err := MustDecimal("1.5").Value()
	if err != nil {
		t.Fatalf("Value(): %v", err)
	}
	if s, ok := v.(string); !ok || s != "1.5" {
		t.Errorf("Value() = %#v, want string \"1.5\"", v)
	}

	var d Decimal
	if err := d.Scan("2.25"); err != nil || d != MustDecimal("2.25") {
		t.Errorf("Scan(string) = %s, %v", d, err)
	}
	if err := d.Scan([]byte("3.75")); err != nil || d != MustDecimal("3.75") {
		t.Errorf("Scan([]byte) = %s, %v", d, err)
	}
	if err := d.Scan(nil); err != nil || !d.IsZero() {
		t.Errorf("Scan(nil) = %s, %v, want zero", d, err)
	}
	if err := d.Scan(int64(5)); err == nil {
		t.Error("Scan(int64) succeeded, want error")
	}
	if err := d.Scan("garbage"); err == nil {
		t.Error("Scan(bad string) succeeded, want error")
	}
}
