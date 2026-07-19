package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// PositionRepo persists the robot's current holdings, one row per
// instrument.
type PositionRepo struct{}

// Upsert inserts p or, if a row already exists for its instrument, replaces
// qty/avg_price/updated_at.
func (PositionRepo) Upsert(ctx context.Context, q Querier, p model.Position) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO positions (instrument_uid, qty, avg_price, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (instrument_uid) DO UPDATE SET
			qty = excluded.qty,
			avg_price = excluded.avg_price,
			updated_at = excluded.updated_at`,
		string(p.InstrumentUID), p.Qty, p.AvgPrice, timeText(p.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("sqlite: upsert position %s: %w", p.InstrumentUID, err)
	}
	return nil
}

// Get returns the position for uid. ok is false if none exists.
func (PositionRepo) Get(ctx context.Context, q Querier, uid model.InstrumentUID) (p model.Position, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT instrument_uid, qty, avg_price, updated_at FROM positions WHERE instrument_uid = ?`, string(uid))
	p, err = scanPosition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Position{}, false, nil
	}
	if err != nil {
		return model.Position{}, false, fmt.Errorf("sqlite: get position %s: %w", uid, err)
	}
	return p, true, nil
}

// List returns every position, ordered by instrument_uid.
func (PositionRepo) List(ctx context.Context, q Querier) ([]model.Position, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT instrument_uid, qty, avg_price, updated_at FROM positions ORDER BY instrument_uid`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list positions: %w", err)
	}
	defer rows.Close()

	var out []model.Position
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list positions: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list positions: %w", err)
	}
	return out, nil
}

func scanPosition(s rowScanner) (model.Position, error) {
	var p model.Position
	var uid, updatedAt string
	if err := s.Scan(&uid, &p.Qty, &p.AvgPrice, &updatedAt); err != nil {
		return model.Position{}, err
	}
	p.InstrumentUID = model.InstrumentUID(uid)
	t, err := parseTimeText(updatedAt)
	if err != nil {
		return model.Position{}, err
	}
	p.UpdatedAt = t
	return p, nil
}
