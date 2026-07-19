package tinvestcli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Event is a marker interface for everything StreamMarketdata delivers on its
// channel. The concrete types are CandleEvent, LastPriceEvent, OrderbookEvent,
// StatusEvent, GapEvent, and StreamDownError. The set is closed (the marker
// method is unexported), so a type switch over it is exhaustive.
type Event interface {
	isStreamEvent()
}

// CandleEvent is a streamed candle. Its event time is CandleTime (the bar's own
// timestamp), never the frame receipt time — collectors key candles by
// (instrument, interval, bar time). A streamed bar is the currently forming one,
// so completeness is unknown and Model reports Complete=false.
type CandleEvent struct {
	InstrumentUID string
	Ticker        string
	ClassCode     string
	FIGI          string
	Interval      string // SubscriptionInterval enum form, e.g. "SUBSCRIPTION_INTERVAL_FIVE_MINUTES"
	Open          model.Decimal
	High          model.Decimal
	Low           model.Decimal
	Close         model.Decimal
	Volume        int64
	VolumeBuy     int64
	VolumeSell    int64
	CandleTime    time.Time
	LastTradeTime time.Time
	Source        string
}

func (CandleEvent) isStreamEvent() {}

// streamIntervalModel maps the stream's SubscriptionInterval enum onto the
// model's interval tokens for the subset the robot trades on. Streamed candles
// carry the SUBSCRIPTION_INTERVAL_* family (a distinct enum from the unary
// candles-get CANDLE_INTERVAL_*); only that family is accepted here — a value
// outside it makes Model report an unsupported-interval ProtocolError.
var streamIntervalModel = map[string]model.CandleInterval{
	"SUBSCRIPTION_INTERVAL_ONE_MINUTE":      model.Interval1m,
	"SUBSCRIPTION_INTERVAL_FIVE_MINUTES":    model.Interval5m,
	"SUBSCRIPTION_INTERVAL_FIFTEEN_MINUTES": model.Interval15m,
	"SUBSCRIPTION_INTERVAL_ONE_HOUR":        model.Interval1h,
	"SUBSCRIPTION_INTERVAL_ONE_DAY":         model.Interval1d,
}

// Model maps the event onto a model.Candle, erroring if the interval is outside
// the supported set. TS is CandleTime; Complete is false (a live bar).
func (e CandleEvent) Model() (model.Candle, error) {
	interval, ok := streamIntervalModel[e.Interval]
	if !ok {
		return model.Candle{}, &ProtocolError{
			Reason: "unsupported stream candle interval",
			Detail: e.Interval,
		}
	}
	return model.Candle{
		InstrumentUID: model.InstrumentUID(e.InstrumentUID),
		Interval:      interval,
		Open:          e.Open,
		High:          e.High,
		Low:           e.Low,
		Close:         e.Close,
		Volume:        e.Volume,
		TS:            e.CandleTime.UTC(),
		Complete:      false,
	}, nil
}

// LastPriceEvent is a streamed last-price tick. Its event time is Time.
type LastPriceEvent struct {
	InstrumentUID string
	Ticker        string
	ClassCode     string
	FIGI          string
	Price         model.Decimal
	PriceType     string
	Time          time.Time
}

func (LastPriceEvent) isStreamEvent() {}

// Quote maps the tick onto a model.Quote (last price only; bid/ask unknown).
func (e LastPriceEvent) Quote() model.Quote {
	return model.Quote{
		InstrumentUID: model.InstrumentUID(e.InstrumentUID),
		Last:          e.Price,
		TS:            e.Time.UTC(),
	}
}

// OrderbookEvent is a streamed order-book snapshot. The robot trades on
// top-of-book, so Bid and Ask are the best (first) levels of each side; a
// snapshot with an empty side leaves that price zero. Its event time is Time.
type OrderbookEvent struct {
	InstrumentUID string
	Ticker        string
	ClassCode     string
	FIGI          string
	Depth         int
	Bid           model.Decimal
	Ask           model.Decimal
	Time          time.Time
}

func (OrderbookEvent) isStreamEvent() {}

// Quote maps the top of book onto a model.Quote carrying the best bid and ask.
// Last stays zero: an order-book snapshot reports no trade price. A consumer
// distinguishes this high-fidelity quote from a last-price fallback via
// Quote.HasBidAsk.
func (e OrderbookEvent) Quote() model.Quote {
	return model.Quote{
		InstrumentUID: model.InstrumentUID(e.InstrumentUID),
		Bid:           e.Bid,
		Ask:           e.Ask,
		TS:            e.Time.UTC(),
	}
}

// StatusKind is a stream lifecycle frame type.
type StatusKind string

const (
	StatusConnected    StatusKind = "connected"
	StatusDisconnected StatusKind = "disconnected"
	StatusResubscribed StatusKind = "resubscribed"
	StatusLagging      StatusKind = "lagging"
	StatusError        StatusKind = "error"
)

// StatusEvent surfaces a lifecycle frame (connected/disconnected/resubscribed/
// lagging) or an in-band error frame. The CLI reconnects internally, so a
// disconnected/connected pair is informational, not a process restart — but a
// non-shutdown disconnect also yields a GapEvent so candles missed during the
// drop get backfilled. Err is set only for error frames.
type StatusEvent struct {
	Kind          StatusKind
	Time          time.Time
	Attempt       int
	Subscriptions int
	Reason        string
	Final         bool
	Err           *BrokerError
}

func (StatusEvent) isStreamEvent() {}

// GapEvent marks a stretch where streamed candles may be missing, so the
// collector can backfill it with a unary candles pull. From is the last known
// good point; To is the far edge when known, otherwise zero (open-ended: up to
// now). InstrumentUID is empty when the gap spans the whole subscription.
type GapEvent struct {
	InstrumentUID string
	From          time.Time
	To            time.Time
	Reason        string
}

func (GapEvent) isStreamEvent() {}

// StreamDownError is the terminal event: the supervisor has given up. It is
// delivered as the last event before the channel closes, and is also a Go error.
// CircuitTripped is true when the breaker fired (too many fast restarts);
// otherwise the cause was a non-restartable classification (auth/usage/schema).
type StreamDownError struct {
	Err            error
	Attempts       int
	CircuitTripped bool
}

func (StreamDownError) isStreamEvent() {}

func (e *StreamDownError) Error() string {
	cause := "unknown cause"
	if e.Err != nil {
		cause = e.Err.Error()
	}
	if e.CircuitTripped {
		return fmt.Sprintf("tinvestcli: stream down, circuit breaker tripped after %d fast restarts: %s", e.Attempts, cause)
	}
	return "tinvestcli: stream down (not restartable): " + cause
}

func (e *StreamDownError) Unwrap() error { return e.Err }

// wireStreamFrame mirrors the NDJSON stream frame shape.
type wireStreamFrame struct {
	Type          string          `json:"type"`
	SchemaVersion string          `json:"schema_version"`
	Time          time.Time       `json:"time"`
	AccountID     string          `json:"account_id"`
	Data          json.RawMessage `json:"data"`
	Error         *wireErrorBody  `json:"error"`
}

// wireStreamCandle mirrors the streamed candle data payload.
type wireStreamCandle struct {
	InstrumentUID string      `json:"instrument_uid"`
	Ticker        string      `json:"ticker"`
	ClassCode     string      `json:"class_code"`
	FIGI          string      `json:"figi"`
	Interval      string      `json:"interval"`
	Open          Money       `json:"open"`
	High          Money       `json:"high"`
	Low           Money       `json:"low"`
	Close         Money       `json:"close"`
	Volume        int64String `json:"volume"`
	VolumeBuy     int64String `json:"volume_buy"`
	VolumeSell    int64String `json:"volume_sell"`
	CandleTime    time.Time   `json:"candle_time"`
	LastTradeTime time.Time   `json:"last_trade_time"`
	Source        string      `json:"source"`
}

// wireStreamLastPrice mirrors the streamed last-price data payload.
type wireStreamLastPrice struct {
	InstrumentUID string    `json:"instrument_uid"`
	Ticker        string    `json:"ticker"`
	ClassCode     string    `json:"class_code"`
	FIGI          string    `json:"figi"`
	Price         Money     `json:"price"`
	PriceType     string    `json:"price_type"`
	Time          time.Time `json:"time"`
}

// wireStreamOrderbook mirrors the streamed order-book data payload
// (render.StreamOrderBookView). Only top-of-book is read; the deeper levels,
// consistency flag, and limits are ignored.
type wireStreamOrderbook struct {
	InstrumentUID string                     `json:"instrument_uid"`
	Ticker        string                     `json:"ticker"`
	ClassCode     string                     `json:"class_code"`
	FIGI          string                     `json:"figi"`
	Depth         int                        `json:"depth"`
	Bids          []wireStreamOrderbookLevel `json:"bids"`
	Asks          []wireStreamOrderbookLevel `json:"asks"`
	Time          time.Time                  `json:"orderbook_time"`
}

// wireStreamOrderbookLevel is one price/quantity level of a book side. Price is
// a quotation (value field); quantity is the contract's string-encoded integer.
type wireStreamOrderbookLevel struct {
	Price    Money       `json:"price"`
	Quantity int64String `json:"quantity"`
}

// wireStreamLifecycle mirrors the lifecycle data payload (connected/disconnected/…).
type wireStreamLifecycle struct {
	Attempt       int    `json:"attempt"`
	Subscriptions int    `json:"subscriptions"`
	Reason        string `json:"reason"`
	Final         bool   `json:"final"`
}
