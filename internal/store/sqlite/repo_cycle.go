package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CycleRepo persists decision-cycle records.
type CycleRepo struct{}

// Insert creates a new cycle row and returns its id.
func (CycleRepo) Insert(ctx context.Context, q Querier, c Cycle) (int64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO cycles (started_at, as_of, mode, engine, engine_version, prompt_template_hash, config_snapshot, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		timeText(c.StartedAt), timeText(c.AsOf), c.Mode, c.Engine, c.EngineVersion, c.PromptTemplateHash, c.ConfigSnapshot, c.Status,
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert cycle: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert cycle: %w", err)
	}
	return id, nil
}

// UpdateStatus sets the status of the cycle with the given id.
func (CycleRepo) UpdateStatus(ctx context.Context, q Querier, id int64, status string) error {
	res, err := q.ExecContext(ctx, `UPDATE cycles SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("sqlite: update cycle %d status: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: update cycle %d status: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns the cycle with the given id, or ErrNotFound.
func (CycleRepo) Get(ctx context.Context, q Querier, id int64) (Cycle, error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, started_at, as_of, mode, engine, engine_version, prompt_template_hash, config_snapshot, status
		FROM cycles WHERE id = ?`, id)
	c, err := scanCycle(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Cycle{}, ErrNotFound
	}
	if err != nil {
		return Cycle{}, fmt.Errorf("sqlite: get cycle %d: %w", id, err)
	}
	return c, nil
}

// Recent returns up to limit cycles, most recently started first.
func (CycleRepo) Recent(ctx context.Context, q Querier, limit int) ([]Cycle, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, started_at, as_of, mode, engine, engine_version, prompt_template_hash, config_snapshot, status
		FROM cycles ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent cycles: %w", err)
	}
	defer rows.Close()

	var out []Cycle
	for rows.Next() {
		c, err := scanCycle(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: recent cycles: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: recent cycles: %w", err)
	}
	return out, nil
}

func scanCycle(s rowScanner) (Cycle, error) {
	var c Cycle
	var startedAt, asOf string
	err := s.Scan(&c.ID, &startedAt, &asOf, &c.Mode, &c.Engine, &c.EngineVersion, &c.PromptTemplateHash, &c.ConfigSnapshot, &c.Status)
	if err != nil {
		return Cycle{}, err
	}
	if c.StartedAt, err = parseTimeText(startedAt); err != nil {
		return Cycle{}, err
	}
	if c.AsOf, err = parseTimeText(asOf); err != nil {
		return Cycle{}, err
	}
	return c, nil
}
