package main

import (
	"fmt"
	"strconv"
	"strings"
)

// decimal is the JSON form of a money value or quotation, mirroring
// render.Decimal: the raw units+nano pair (units as a string) next to a
// normalized decimal string. Currency is set for money values only.
type decimal struct {
	Units    string `json:"units"`
	Nano     int32  `json:"nano"`
	Value    string `json:"value"`
	Currency string `json:"currency,omitempty"`
}

// quotationDecimal parses an exact decimal string ("270.5", "-0.001") into the
// contract's units+nano+value shape. It mirrors render.ParseQuotation followed
// by render.Quotation, so the fake emits the same three fields the real CLI
// would. An empty string yields a zero decimal.
func quotationDecimal(s string) (decimal, error) {
	if strings.TrimSpace(s) == "" {
		return decimal{Units: "0", Nano: 0, Value: "0"}, nil
	}
	units, nano, err := parseUnitsNano(s)
	if err != nil {
		return decimal{}, err
	}
	return decimal{
		Units: strconv.FormatInt(units, 10),
		Nano:  nano,
		Value: decimalString(units, nano),
	}, nil
}

// moneyDecimal is quotationDecimal with a currency attached, mirroring
// render.Money.
func moneyDecimal(s, currency string) (decimal, error) {
	d, err := quotationDecimal(s)
	if err != nil {
		return decimal{}, err
	}
	d.Currency = currency
	return d, nil
}

// parseUnitsNano is the inverse of decimalString: it splits an exact decimal
// string into an int64 units and int32 nano sharing one sign, rejecting more
// than nine fractional digits rather than truncating (as render.ParseQuotation
// does).
func parseUnitsNano(s string) (int64, int32, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, 0, fmt.Errorf("empty decimal")
	}
	negative := false
	switch trimmed[0] {
	case '+':
		trimmed = trimmed[1:]
	case '-':
		negative = true
		trimmed = trimmed[1:]
	}
	if trimmed == "" {
		return 0, 0, fmt.Errorf("invalid decimal %q", s)
	}

	intPart, fracPart := trimmed, ""
	if dot := strings.IndexByte(trimmed, '.'); dot >= 0 {
		intPart = trimmed[:dot]
		fracPart = trimmed[dot+1:]
	}
	if intPart == "" {
		if fracPart == "" {
			return 0, 0, fmt.Errorf("invalid decimal %q", s)
		}
		intPart = "0"
	}
	if !allDigits(intPart) {
		return 0, 0, fmt.Errorf("invalid decimal %q: integer part must contain digits only", s)
	}
	if len(fracPart) > 9 {
		return 0, 0, fmt.Errorf("decimal %q has more than 9 fractional digits", s)
	}
	if !allDigits(fracPart) {
		return 0, 0, fmt.Errorf("invalid decimal %q: fractional part must contain digits only", s)
	}

	units, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid decimal %q: %w", s, err)
	}
	var nano int64
	if fracPart != "" {
		padded := fracPart + strings.Repeat("0", 9-len(fracPart))
		nano, err = strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid decimal %q: %w", s, err)
		}
	}
	if negative {
		units, nano = -units, -nano
	}
	return units, int32(nano), nil
}

// decimalString renders units+nano as an exact decimal string with trailing
// fractional zeros trimmed, mirroring render.DecimalString.
func decimalString(units int64, nano int32) string {
	negative := units < 0 || nano < 0
	absUnits := units
	if absUnits < 0 {
		absUnits = -absUnits
	}
	absNano := int64(nano)
	if absNano < 0 {
		absNano = -absNano
	}
	var b strings.Builder
	if negative && (absUnits != 0 || absNano != 0) {
		b.WriteByte('-')
	}
	b.WriteString(strconv.FormatInt(absUnits, 10))
	if absNano != 0 {
		frac := strings.TrimRight(pad9(absNano), "0")
		b.WriteByte('.')
		b.WriteString(frac)
	}
	return b.String()
}

func pad9(n int64) string {
	s := strconv.FormatInt(n, 10)
	return strings.Repeat("0", 9-len(s)) + s
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
