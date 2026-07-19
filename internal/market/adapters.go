package market

import (
	"context"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
)

// clientBroker adapts *tinvestcli.Client to Broker, wrapping the concrete
// *Stream return in the StreamHandle interface.
type clientBroker struct{ c *tinvestcli.Client }

// NewClientBroker adapts a tinvest client to the collector's Broker port.
func NewClientBroker(c *tinvestcli.Client) Broker { return clientBroker{c: c} }

func (b clientBroker) InstrumentGet(ctx context.Context, id string) (tinvestcli.Instrument, error) {
	return b.c.InstrumentGet(ctx, id)
}

func (b clientBroker) CandlesGet(ctx context.Context, id string, interval model.CandleInterval, from, to time.Time) (tinvestcli.CandlesResult, error) {
	return b.c.CandlesGet(ctx, id, interval, from, to)
}

func (b clientBroker) StreamMarketdata(ctx context.Context, req tinvestcli.StreamRequest) (StreamHandle, error) {
	s, err := b.c.StreamMarketdata(ctx, req)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// SQLiteStore adapts the sqlite repositories to the collector's store ports,
// binding each stateless repo to the single *sqlite.DB connection. One value
// satisfies InstrumentSink, CandleStore, QuoteSink, and EventLog.
type SQLiteStore struct {
	db    *sqlite.DB
	inst  sqlite.InstrumentRepo
	cand  sqlite.CandleRepo
	quote sqlite.QuoteRepo
	event sqlite.EventRepo
}

// NewSQLiteStore binds the collector's store ports to db.
func NewSQLiteStore(db *sqlite.DB) *SQLiteStore { return &SQLiteStore{db: db} }

func (s *SQLiteStore) UpsertInstrument(ctx context.Context, i model.Instrument, cachedAt time.Time) error {
	return s.inst.Upsert(ctx, s.db, i, cachedAt)
}

func (s *SQLiteStore) LatestComplete(ctx context.Context, uid model.InstrumentUID, interval model.CandleInterval) (model.Candle, bool, error) {
	return s.cand.LatestComplete(ctx, s.db, uid, interval)
}

func (s *SQLiteStore) UpsertCandle(ctx context.Context, c model.Candle) error {
	return s.cand.Upsert(ctx, s.db, c)
}

func (s *SQLiteStore) InsertQuote(ctx context.Context, q model.Quote) error {
	return s.quote.Insert(ctx, s.db, q)
}

func (s *SQLiteStore) Log(ctx context.Context, ev LogEvent) error {
	_, err := s.event.Insert(ctx, s.db, sqlite.Event{
		TS:      ev.TS,
		Level:   string(ev.Level),
		Code:    ev.Code,
		Payload: ev.Payload,
	})
	return err
}
