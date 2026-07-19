package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// CandleRepo persists OHLCV bars.
type CandleRepo struct{}

// Upsert inserts c or, if a row already exists for its
// (instrument_uid, interval, ts), replaces the OHLCV/complete columns.
//
// One conflict case is deliberately a no-op: an incomplete candle never
// overwrites a stored complete one. A complete bar is a decision watermark
// with final OHLCV; a delayed stream update for the same timestamp arriving
// after backfill (or a late partial frame) must not regress complete=1 back to
// 0 or replace final values with partial ones. Complete→complete corrections
// are still allowed (a restated final bar), and incomplete→incomplete /
// incomplete→complete updates proceed normally. The guard is the ON CONFLICT
// WHERE clause below.
func (CandleRepo) Upsert(ctx context.Context, q Querier, c model.Candle) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO candles (instrument_uid, interval, open, high, low, close, volume, ts, complete)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (instrument_uid, interval, ts) DO UPDATE SET
			open = excluded.open,
			high = excluded.high,
			low = excluded.low,
			close = excluded.close,
			volume = excluded.volume,
			complete = excluded.complete
		WHERE excluded.complete = 1 OR candles.complete = 0`,
		string(c.InstrumentUID), c.Interval.String(), c.Open, c.High, c.Low, c.Close, c.Volume, timeText(c.TS), boolToInt(c.Complete),
	)
	if err != nil {
		return fmt.Errorf("sqlite: upsert candle %s %s %s: %w", c.InstrumentUID, c.Interval, c.TS, err)
	}
	return nil
}

// Range returns candles for uid/interval with ts in [from, to], ascending by
// ts.
func (CandleRepo) Range(ctx context.Context, q Querier, uid model.InstrumentUID, interval model.CandleInterval, from, to time.Time) ([]model.Candle, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT instrument_uid, interval, open, high, low, close, volume, ts, complete
		FROM candles
		WHERE instrument_uid = ? AND interval = ? AND ts >= ? AND ts <= ?
		ORDER BY ts ASC, id ASC`,
		string(uid), interval.String(), timeText(from), timeText(to),
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: range candles %s %s: %w", uid, interval, err)
	}
	defer rows.Close()

	var out []model.Candle
	for rows.Next() {
		c, err := scanCandle(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: range candles %s %s: %w", uid, interval, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: range candles %s %s: %w", uid, interval, err)
	}
	return out, nil
}

// LatestComplete returns the most recent candle with complete = true for
// uid/interval. ok is false if none exists.
func (CandleRepo) LatestComplete(ctx context.Context, q Querier, uid model.InstrumentUID, interval model.CandleInterval) (c model.Candle, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT instrument_uid, interval, open, high, low, close, volume, ts, complete
		FROM candles
		WHERE instrument_uid = ? AND interval = ? AND complete = 1
		ORDER BY ts DESC, id DESC LIMIT 1`,
		string(uid), interval.String(),
	)
	c, err = scanCandle(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Candle{}, false, nil
	}
	if err != nil {
		return model.Candle{}, false, fmt.Errorf("sqlite: latest complete candle %s %s: %w", uid, interval, err)
	}
	return c, true, nil
}

// LatestCompleteAsOf returns the most recent candle with complete = true for
// uid/interval whose ts is at or before asOf. It is LatestComplete's
// point-in-time counterpart: the cycle orchestrator's assemble step must read
// market state strictly at-or-before the decision's as_of instant so a bar
// that only became known after that instant can never leak into the
// decision. ok is false if none exists.
func (CandleRepo) LatestCompleteAsOf(ctx context.Context, q Querier, uid model.InstrumentUID, interval model.CandleInterval, asOf time.Time) (c model.Candle, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT instrument_uid, interval, open, high, low, close, volume, ts, complete
		FROM candles
		WHERE instrument_uid = ? AND interval = ? AND complete = 1 AND ts <= ?
		ORDER BY ts DESC, id DESC LIMIT 1`,
		string(uid), interval.String(), timeText(asOf),
	)
	c, err = scanCandle(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Candle{}, false, nil
	}
	if err != nil {
		return model.Candle{}, false, fmt.Errorf("sqlite: latest complete candle as of %s %s %s: %w", uid, interval, asOf, err)
	}
	return c, true, nil
}

func scanCandle(s rowScanner) (model.Candle, error) {
	var c model.Candle
	var uid, interval, ts string
	var complete int
	err := s.Scan(&uid, &interval, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume, &ts, &complete)
	if err != nil {
		return model.Candle{}, err
	}
	c.InstrumentUID = model.InstrumentUID(uid)
	iv, err := model.ParseCandleInterval(interval)
	if err != nil {
		return model.Candle{}, err
	}
	c.Interval = iv
	c.TS, err = parseTimeText(ts)
	if err != nil {
		return model.Candle{}, err
	}
	c.Complete = complete != 0
	return c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
