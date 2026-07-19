package market

import (
	"context"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
)

// Broker is the subset of the tinvest client the collector needs. *tinvestcli.Client
// satisfies it through NewClientBroker (which adapts the concrete *Stream return
// to StreamHandle).
type Broker interface {
	InstrumentGet(ctx context.Context, id string) (tinvestcli.Instrument, error)
	CandlesGet(ctx context.Context, id string, interval model.CandleInterval, from, to time.Time) (tinvestcli.CandlesResult, error)
	StreamMarketdata(ctx context.Context, req tinvestcli.StreamRequest) (StreamHandle, error)
}

// StreamHandle is a running marketdata stream: a channel of typed events and a
// shutdown that reaps the child. *tinvestcli.Stream satisfies it.
type StreamHandle interface {
	Events() <-chan tinvestcli.Event
	Close() error
}

// InstrumentSink persists the instrument reference cache. Methods are named
// per-noun (not a bare Upsert) so one adapter value can satisfy every store
// port without method-name collisions.
type InstrumentSink interface {
	UpsertInstrument(ctx context.Context, i model.Instrument, cachedAt time.Time) error
}

// CandleStore persists and reads back candle bars. UpsertCandle honors the
// store's complete-bar guard (an incomplete bar never clobbers a stored
// complete one).
type CandleStore interface {
	UpsertCandle(ctx context.Context, c model.Candle) error
	LatestComplete(ctx context.Context, uid model.InstrumentUID, interval model.CandleInterval) (model.Candle, bool, error)
}

// QuoteSink appends top-of-book snapshots.
type QuoteSink interface {
	InsertQuote(ctx context.Context, q model.Quote) error
}

// Level is an event severity for the structured log.
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// LogEvent is one structured collector event. TS is stamped by the collector
// from its clock.
type LogEvent struct {
	TS      time.Time
	Level   Level
	Code    string
	Payload string
}

// EventLog records structured collector events (DESIGN §5/§12). Logging is
// best-effort: a failure here must not itself break collection.
type EventLog interface {
	Log(ctx context.Context, ev LogEvent) error
}
