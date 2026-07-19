package model

import (
	"fmt"
	"time"
)

// CandleInterval is a supported candle width, string-backed to match the
// tinvest CLI's interval tokens.
type CandleInterval string

const (
	Interval1m  CandleInterval = "1m"
	Interval5m  CandleInterval = "5m"
	Interval15m CandleInterval = "15m"
	Interval1h  CandleInterval = "1h"
	Interval1d  CandleInterval = "1d"
)

// String returns the interval token.
func (c CandleInterval) String() string { return string(c) }

// Valid reports whether c is one of the supported intervals.
func (c CandleInterval) Valid() bool {
	switch c {
	case Interval1m, Interval5m, Interval15m, Interval1h, Interval1d:
		return true
	default:
		return false
	}
}

// ParseCandleInterval parses an interval token, erroring on anything unknown.
func ParseCandleInterval(s string) (CandleInterval, error) {
	c := CandleInterval(s)
	if !c.Valid() {
		return "", fmt.Errorf("model: invalid candle interval %q", s)
	}
	return c, nil
}

// Candle is one OHLCV bar. TS is the bar's open time in UTC. Complete is false
// for the still-forming current bar; decisions consume only complete candles.
type Candle struct {
	InstrumentUID InstrumentUID
	Interval      CandleInterval
	Open          Decimal
	High          Decimal
	Low           Decimal
	Close         Decimal
	Volume        int64
	TS            time.Time
	Complete      bool
}

// Quote is a point-in-time top-of-book snapshot. A zero Decimal in any price
// field means that side is unknown; use HasBidAsk before relying on the
// spread.
type Quote struct {
	InstrumentUID InstrumentUID
	Bid           Decimal
	Ask           Decimal
	Last          Decimal
	TS            time.Time
}

// HasBidAsk reports whether both the bid and ask are known (non-zero).
func (q Quote) HasBidAsk() bool { return !q.Bid.IsZero() && !q.Ask.IsZero() }
