package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// ExecSession is the persisted current trading-session window. A zero Start and
// End is the documented 24-hour-open default.
type ExecSession struct {
	Start time.Time
	End   time.Time
}

// ExecSessionRepo persists the single current trading-session window the paper
// simulator gates fills on, so the window survives a restart independently of
// the next Submit (DESIGN §7).
type ExecSessionRepo struct{}

// Upsert replaces the current session window, stamping updatedAt.
func (ExecSessionRepo) Upsert(ctx context.Context, q Querier, sess ExecSession, updatedAt time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO execution_session (id, session_start, session_end, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_start = excluded.session_start,
			session_end   = excluded.session_end,
			updated_at    = excluded.updated_at`,
		timeText(sess.Start), timeText(sess.End), timeText(updatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: upsert execution session: %w", err)
	}
	return nil
}

// Get returns the current session window; found is false when none is stored.
func (ExecSessionRepo) Get(ctx context.Context, q Querier) (ExecSession, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT session_start, session_end FROM execution_session WHERE id = 1`)
	var start, end string
	if err := row.Scan(&start, &end); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExecSession{}, false, nil
		}
		return ExecSession{}, false, fmt.Errorf("sqlite: get execution session: %w", err)
	}
	s, err := parseTimeText(start)
	if err != nil {
		return ExecSession{}, false, fmt.Errorf("sqlite: get execution session start: %w", err)
	}
	e, err := parseTimeText(end)
	if err != nil {
		return ExecSession{}, false, fmt.Errorf("sqlite: get execution session end: %w", err)
	}
	return ExecSession{Start: s, End: e}, true, nil
}

// TradingStatus is the persisted authoritative trading permissions for one
// instrument. Status is a free-form broker status token (informational);
// BuyAvailable and SellAvailable are the per-side permissions the simulator
// gates fills on.
type TradingStatus struct {
	InstrumentUID model.InstrumentUID
	Status        string
	BuyAvailable  bool
	SellAvailable bool
}

// TradingStatusRepo persists per-instrument trading permissions so a suspended
// or side-disabled instrument is not filled after a restart.
type TradingStatusRepo struct{}

// Upsert replaces the trading status for an instrument, stamping updatedAt.
func (TradingStatusRepo) Upsert(ctx context.Context, q Querier, ts TradingStatus, updatedAt time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO instrument_trading_status (instrument_uid, status, buy_available, sell_available, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(instrument_uid) DO UPDATE SET
			status         = excluded.status,
			buy_available  = excluded.buy_available,
			sell_available = excluded.sell_available,
			updated_at     = excluded.updated_at`,
		string(ts.InstrumentUID), ts.Status, ts.BuyAvailable, ts.SellAvailable, timeText(updatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: upsert trading status for %s: %w", ts.InstrumentUID, err)
	}
	return nil
}

// Get returns the trading status for uid; found is false when none is stored
// (the caller treats an absent status as unrestricted).
func (TradingStatusRepo) Get(ctx context.Context, q Querier, uid model.InstrumentUID) (TradingStatus, bool, error) {
	row := q.QueryRowContext(ctx,
		`SELECT status, buy_available, sell_available FROM instrument_trading_status WHERE instrument_uid = ?`, string(uid))
	var status string
	var buy, sell int
	if err := row.Scan(&status, &buy, &sell); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TradingStatus{}, false, nil
		}
		return TradingStatus{}, false, fmt.Errorf("sqlite: get trading status for %s: %w", uid, err)
	}
	return TradingStatus{InstrumentUID: uid, Status: status, BuyAvailable: buy != 0, SellAvailable: sell != 0}, true, nil
}
