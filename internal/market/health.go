package market

import (
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Health is a point-in-time view of the collector's data freshness and stream
// state. It feeds the decision request's data-freshness fields and the TUI.
type Health struct {
	// StreamUp is true while the collector holds a live stream; it goes false on
	// a terminal stream-down (auth/usage) or between restart attempts.
	StreamUp bool
	// StreamRestarts counts collector-level stream reconnects since Start.
	StreamRestarts int
	// StreamDownReason is the last terminal/last stream-down cause, if any.
	StreamDownReason string
	// LastStreamEvent is the receipt time of the most recent stream event of any
	// kind.
	LastStreamEvent time.Time
	// Instruments is keyed by instrument uid.
	Instruments map[model.InstrumentUID]InstrumentHealth
}

// InstrumentHealth is per-instrument freshness.
type InstrumentHealth struct {
	Ticker string
	// CandleWatermark is the timestamp of the latest stored complete bar.
	CandleWatermark time.Time
	// LastCandleEvent is the candle_time of the most recent streamed forming bar.
	LastCandleEvent time.Time
	// LastQuote is the time of the most recent last-price tick.
	LastQuote time.Time
	// QuoteAge is now-LastQuote at snapshot time; zero when no quote has arrived.
	QuoteAge time.Duration
	// Stale is set when a data operation for this instrument last failed and has
	// not since recovered.
	Stale bool
}

// instHealth is the collector's mutable per-instrument health state.
type instHealth struct {
	ticker          string
	watermark       time.Time
	lastCandleEvent time.Time
	lastQuote       time.Time
	stale           bool
}

// quoteState is the collector's last-known composite top-of-book per instrument.
// Last-price and order-book frames each carry only part of a quote (last, or
// bid/ask), so the collector merges them here and persists complete rows rather
// than letting one kind clobber the other's fields. ts is monotonic (the newest
// observation), so the most recently written row always wins a Latest read.
type quoteState struct {
	bid  model.Decimal
	ask  model.Decimal
	last model.Decimal
	ts   time.Time
}
