package sqlite

import (
	"context"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// FillRepo persists executions against order intents.
type FillRepo struct{}

// Insert appends a new fill row.
func (FillRepo) Insert(ctx context.Context, q Querier, f model.Fill) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO fills (order_intent_id, price, qty, fee, ts) VALUES (?, ?, ?, ?, ?)`,
		f.IntentID, f.Price, f.Qty, f.Fee, timeText(f.TS),
	)
	if err != nil {
		return fmt.Errorf("sqlite: insert fill for intent %s: %w", f.IntentID, err)
	}
	return nil
}

// ListByIntent returns every fill recorded against intentID, oldest first.
func (FillRepo) ListByIntent(ctx context.Context, q Querier, intentID string) ([]model.Fill, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT order_intent_id, price, qty, fee, ts FROM fills
		WHERE order_intent_id = ? ORDER BY ts ASC, id ASC`, intentID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list fills for intent %s: %w", intentID, err)
	}
	defer rows.Close()

	var out []model.Fill
	for rows.Next() {
		var f model.Fill
		var ts string
		if err := rows.Scan(&f.IntentID, &f.Price, &f.Qty, &f.Fee, &ts); err != nil {
			return nil, fmt.Errorf("sqlite: list fills for intent %s: %w", intentID, err)
		}
		if f.TS, err = parseTimeText(ts); err != nil {
			return nil, fmt.Errorf("sqlite: list fills for intent %s: %w", intentID, err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list fills for intent %s: %w", intentID, err)
	}
	return out, nil
}
