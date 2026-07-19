package model

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// scaleDigits is the number of fractional decimal digits Decimal keeps: nano
// precision (1e-9), matching T-Bank's Quotation/MoneyValue (units + nano) and
// the decimal strings the tinvest CLI emits.
const scaleDigits = 9

// nanoScale is 10^scaleDigits, the divisor between the int64 mantissa and the
// value it represents.
const nanoScale = 1_000_000_000

// ErrOverflow is returned by the checked arithmetic methods (Add, Sub, MulInt,
// MulBps, MulRat, RoundToIncrement) when a result does not fit in the int64
// mantissa.
var ErrOverflow = errors.New("model: decimal overflow")

// Decimal is a fixed-point signed decimal with nine fractional digits, stored
// as an int64 mantissa (value = mantissa / 1e9). Its representable range is
// -9223372036.854775807 .. 9223372036.854775807. The zero value is 0.
//
// Overflow policy: the arithmetic that can overflow the mantissa is checked
// and returns an error (Add, Sub, MulInt, MulBps, MulRat, RoundToIncrement);
// the total operations that cannot overflow a validly constructed value (Neg,
// Abs, Cmp, Sign, IsZero) do not. A Decimal only ever holds a mantissa in
// [-MaxInt64, MaxInt64] because construction goes through ParseDecimal, which
// rejects the MinInt64 boundary.
type Decimal struct {
	nano int64
}

// ParseDecimal parses a canonical decimal string: an optional sign, a
// mandatory integer part, and an optional fractional part of at most nine
// digits. It rejects empty input, a bare or trailing decimal point, non-digit
// characters, more than nine fractional digits (no silent truncation), and
// values outside the int64-mantissa range.
func ParseDecimal(s string) (Decimal, error) {
	if s == "" {
		return Decimal{}, fmt.Errorf("model: parse decimal: empty string")
	}
	neg := false
	body := s
	switch body[0] {
	case '+':
		body = body[1:]
	case '-':
		neg = true
		body = body[1:]
	}
	if body == "" {
		return Decimal{}, fmt.Errorf("model: parse decimal %q: sign without digits", s)
	}

	intPart, fracPart := body, ""
	if dot := strings.IndexByte(body, '.'); dot >= 0 {
		intPart = body[:dot]
		fracPart = body[dot+1:]
		if fracPart == "" {
			return Decimal{}, fmt.Errorf("model: parse decimal %q: trailing decimal point", s)
		}
	}
	if intPart == "" {
		return Decimal{}, fmt.Errorf("model: parse decimal %q: missing integer part", s)
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return Decimal{}, fmt.Errorf("model: parse decimal %q: invalid characters", s)
	}
	if len(fracPart) > scaleDigits {
		return Decimal{}, fmt.Errorf("model: parse decimal %q: more than %d fractional digits", s, scaleDigits)
	}

	// Concatenate the integer part with the fraction padded to full nano
	// width, then parse as one integer so strconv reports range overflow for
	// us. Leading zeros are harmless.
	digits := intPart + fracPart + strings.Repeat("0", scaleDigits-len(fracPart))
	mant, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Decimal{}, fmt.Errorf("model: parse decimal %q: out of range", s)
	}
	if neg {
		mant = -mant
	}
	return Decimal{nano: mant}, nil
}

// MustDecimal is ParseDecimal for constants and tests; it panics on error.
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// String renders the canonical form: no exponent, trailing fractional zeros
// trimmed, "0" for zero. It round-trips with ParseDecimal.
func (d Decimal) String() string {
	if d.nano == 0 {
		return "0"
	}
	neg := d.nano < 0
	// Take the magnitude via uint64 so the MinInt64 boundary can never panic,
	// even though ParseDecimal never produces it.
	var mag uint64
	if neg {
		mag = uint64(-(d.nano + 1)) + 1
	} else {
		mag = uint64(d.nano)
	}

	intPart := mag / nanoScale
	fracPart := mag % nanoScale

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(strconv.FormatUint(intPart, 10))
	if fracPart != 0 {
		frac := strings.TrimRight(fmt.Sprintf("%09d", fracPart), "0")
		b.WriteByte('.')
		b.WriteString(frac)
	}
	return b.String()
}

// Float64 returns the value as a float64. It is lossy and intended only for
// indicator math, never for money bookkeeping.
func (d Decimal) Float64() float64 { return float64(d.nano) / nanoScale }

// IsZero reports whether d is exactly zero.
func (d Decimal) IsZero() bool { return d.nano == 0 }

// Sign returns -1, 0, or +1.
func (d Decimal) Sign() int {
	switch {
	case d.nano < 0:
		return -1
	case d.nano > 0:
		return 1
	default:
		return 0
	}
}

// Cmp compares d with o: -1 if d < o, 0 if equal, +1 if d > o.
func (d Decimal) Cmp(o Decimal) int {
	switch {
	case d.nano < o.nano:
		return -1
	case d.nano > o.nano:
		return 1
	default:
		return 0
	}
}

// Neg returns -d. It cannot overflow a validly constructed Decimal.
func (d Decimal) Neg() Decimal { return Decimal{nano: -d.nano} }

// Abs returns |d|. It cannot overflow a validly constructed Decimal.
func (d Decimal) Abs() Decimal {
	if d.nano < 0 {
		return Decimal{nano: -d.nano}
	}
	return d
}

// Add returns d + o, or ErrOverflow if the sum leaves the mantissa range.
func (d Decimal) Add(o Decimal) (Decimal, error) {
	sum := d.nano + o.nano
	if sum == math.MinInt64 || (o.nano > 0 && sum < d.nano) || (o.nano < 0 && sum > d.nano) {
		return Decimal{}, fmt.Errorf("model: Add(%s + %s): %w", d, o, ErrOverflow)
	}
	return Decimal{nano: sum}, nil
}

// Sub returns d - o, or ErrOverflow if the difference leaves the mantissa
// range.
func (d Decimal) Sub(o Decimal) (Decimal, error) {
	diff := d.nano - o.nano
	if diff == math.MinInt64 || (o.nano < 0 && diff < d.nano) || (o.nano > 0 && diff > d.nano) {
		return Decimal{}, fmt.Errorf("model: Sub(%s - %s): %w", d, o, ErrOverflow)
	}
	return Decimal{nano: diff}, nil
}

// MulInt returns d * k, or ErrOverflow if the product leaves the mantissa
// range.
func (d Decimal) MulInt(k int64) (Decimal, error) {
	if k == 0 || d.nano == 0 {
		return Decimal{}, nil
	}
	prod := d.nano * k
	if prod == math.MinInt64 || prod/k != d.nano {
		return Decimal{}, fmt.Errorf("model: MulInt(%s * %d): %w", d, k, ErrOverflow)
	}
	return Decimal{nano: prod}, nil
}

// Rounding selects how MulRat and MulBps resolve a non-integer nano result.
type Rounding int

const (
	// RoundHalfAwayFromZero rounds to nearest, ties away from zero. This is
	// the mode money paths use (e.g. commission = notional * rate).
	RoundHalfAwayFromZero Rounding = iota
	// RoundFloor rounds toward negative infinity.
	RoundFloor
	// RoundCeil rounds toward positive infinity.
	RoundCeil
	// RoundTowardZero truncates (drops the fraction).
	RoundTowardZero
)

// MulBps returns d * bps / 10000 rounded half away from zero — d scaled by a
// basis-point figure (e.g. slippage or a rate quoted in bps). ErrOverflow if
// the result leaves the mantissa range.
func (d Decimal) MulBps(bps int64) (Decimal, error) {
	return d.MulRat(bps, 10000, RoundHalfAwayFromZero)
}

// MulRat returns d * num / den under the given rounding mode, computed with
// big.Int intermediates so no overflow occurs mid-calculation. It errors on a
// zero denominator or an out-of-range result.
func (d Decimal) MulRat(num, den int64, mode Rounding) (Decimal, error) {
	if den == 0 {
		return Decimal{}, fmt.Errorf("model: MulRat: division by zero")
	}
	n := new(big.Int).Mul(big.NewInt(d.nano), big.NewInt(num))
	dd := big.NewInt(den)
	if dd.Sign() < 0 { // keep denominator positive; remainder then tracks n's sign
		n.Neg(n)
		dd.Neg(dd)
	}
	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(n, dd, r) // truncated toward zero; r has the sign of n
	if r.Sign() != 0 {
		roundQuotient(q, r, dd, mode)
	}
	if !q.IsInt64() || q.Int64() == math.MinInt64 {
		return Decimal{}, fmt.Errorf("model: MulRat(%s * %d/%d): %w", d, num, den, ErrOverflow)
	}
	return Decimal{nano: q.Int64()}, nil
}

// roundQuotient adjusts the truncated quotient q in place given the non-zero
// remainder r and positive denominator den.
func roundQuotient(q, r, den *big.Int, mode Rounding) {
	sign := int64(r.Sign()) // +1 or -1: the sign of the exact result
	switch mode {
	case RoundTowardZero:
		// q is already truncated toward zero.
	case RoundFloor:
		if sign < 0 {
			q.Sub(q, big.NewInt(1))
		}
	case RoundCeil:
		if sign > 0 {
			q.Add(q, big.NewInt(1))
		}
	default: // RoundHalfAwayFromZero
		twoAbsR := new(big.Int).Abs(r)
		twoAbsR.Lsh(twoAbsR, 1)
		if twoAbsR.Cmp(den) >= 0 {
			q.Add(q, big.NewInt(sign))
		}
	}
}

// Direction selects how RoundToIncrement aligns a value to a price tick.
type Direction int

const (
	// Floor rounds down to the nearest lower multiple of the increment. The
	// fill/risk code uses Floor for buy limit prices (never pay above intent).
	Floor Direction = iota
	// Ceil rounds up to the nearest higher multiple. Used for sell limit
	// prices (never accept below intent).
	Ceil
	// Nearest rounds to the nearest multiple, ties away from zero.
	Nearest
)

// RoundToIncrement returns d aligned to a multiple of inc under dir. inc must
// be positive; a value already on a tick is returned unchanged. ErrOverflow if
// the aligned result leaves the mantissa range.
func (d Decimal) RoundToIncrement(inc Decimal, dir Direction) (Decimal, error) {
	if inc.nano <= 0 {
		return Decimal{}, fmt.Errorf("model: RoundToIncrement: increment must be positive, got %s", inc)
	}
	q := d.nano / inc.nano
	rem := d.nano % inc.nano
	if rem == 0 {
		return d, nil // exact on tick
	}
	switch dir {
	case Floor:
		if d.nano < 0 {
			q--
		}
	case Ceil:
		if d.nano > 0 {
			q++
		}
	case Nearest:
		absRem := rem
		if absRem < 0 {
			absRem = -absRem
		}
		if absRem >= inc.nano-absRem { // 2*|rem| >= inc, without overflowing
			if d.nano > 0 {
				q++
			} else {
				q--
			}
		}
	default:
		return Decimal{}, fmt.Errorf("model: RoundToIncrement: unknown direction %d", dir)
	}
	res := q * inc.nano
	if res == math.MinInt64 || res/inc.nano != q {
		return Decimal{}, fmt.Errorf("model: RoundToIncrement(%s): %w", d, ErrOverflow)
	}
	return Decimal{nano: res}, nil
}

// MarshalJSON encodes the Decimal as a JSON string. The wire contract is
// strings, never JSON numbers.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(d.String())), nil
}

// UnmarshalJSON decodes a Decimal from a JSON string, rejecting JSON numbers
// (and null) so the string contract is enforced at the boundary.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] != '"' {
		return fmt.Errorf("model: Decimal must be a JSON string, got %s", b)
	}
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return fmt.Errorf("model: Decimal invalid JSON string: %w", err)
	}
	parsed, err := ParseDecimal(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// Value implements driver.Valuer, storing the Decimal as its canonical TEXT
// form.
func (d Decimal) Value() (driver.Value, error) { return d.String(), nil }

// Scan implements sql.Scanner, reading the canonical TEXT form (string or
// []byte). A SQL NULL scans as the zero Decimal.
func (d *Decimal) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*d = Decimal{}
		return nil
	case string:
		parsed, err := ParseDecimal(v)
		if err != nil {
			return err
		}
		*d = parsed
		return nil
	case []byte:
		parsed, err := ParseDecimal(string(v))
		if err != nil {
			return err
		}
		*d = parsed
		return nil
	default:
		return fmt.Errorf("model: cannot scan %T into Decimal", src)
	}
}
