package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EquityRepo persists points on the equity curve (DESIGN §5).
type EquityRepo struct{}

// Insert appends a new equity snapshot and returns its id.
func (EquityRepo) Insert(ctx context.Context, q Querier, s EquitySnapshot) (int64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO equity_snapshots (ts, cash, market_value, total) VALUES (?, ?, ?, ?)`,
		timeText(s.TS), s.Cash, s.MarketValue, s.Total,
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert equity snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert equity snapshot: %w", err)
	}
	return id, nil
}

// Latest returns the most recent equity snapshot. ok is false if none
// exists.
func (EquityRepo) Latest(ctx context.Context, q Querier) (s EquitySnapshot, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, ts, cash, market_value, total FROM equity_snapshots ORDER BY ts DESC, id DESC LIMIT 1`)
	s, err = scanEquitySnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EquitySnapshot{}, false, nil
	}
	if err != nil {
		return EquitySnapshot{}, false, fmt.Errorf("sqlite: latest equity snapshot: %w", err)
	}
	return s, true, nil
}

// Range returns equity snapshots with ts in [from, to], ascending by ts —
// the series behind the equity curve.
func (EquityRepo) Range(ctx context.Context, q Querier, from, to time.Time) ([]EquitySnapshot, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, ts, cash, market_value, total FROM equity_snapshots
		WHERE ts >= ? AND ts <= ? ORDER BY ts ASC, id ASC`, timeText(from), timeText(to))
	if err != nil {
		return nil, fmt.Errorf("sqlite: range equity snapshots: %w", err)
	}
	defer rows.Close()

	var out []EquitySnapshot
	for rows.Next() {
		s, err := scanEquitySnapshot(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: range equity snapshots: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: range equity snapshots: %w", err)
	}
	return out, nil
}

func scanEquitySnapshot(s rowScanner) (EquitySnapshot, error) {
	var e EquitySnapshot
	var ts string
	if err := s.Scan(&e.ID, &ts, &e.Cash, &e.MarketValue, &e.Total); err != nil {
		return EquitySnapshot{}, err
	}
	t, err := parseTimeText(ts)
	if err != nil {
		return EquitySnapshot{}, err
	}
	e.TS = t
	return e, nil
}
