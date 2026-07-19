package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// IntentRepo persists order intents — the durable record written before any
// broker call, keyed by a stable client order id (DESIGN §4, §5).
type IntentRepo struct{}

// Insert creates a new order_intents row. ClientOrderID must be unique.
func (IntentRepo) Insert(ctx context.Context, q Querier, in model.OrderIntent) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO order_intents (client_order_id, decision_id, instrument_uid, side, qty, type, limit_price, time_in_force, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ClientOrderID, in.DecisionID, string(in.InstrumentUID), in.Side.String(), in.Qty, in.Type.String(),
		in.LimitPrice, in.TimeInForce.String(), in.State.String(), timeText(in.CreatedAt), timeText(in.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("sqlite: insert order intent %s: %w", in.ClientOrderID, err)
	}
	return nil
}

// UpdateState sets the state and updated_at of the intent with the given
// client order id. The caller is responsible for state-machine validation
// (model.CanTransition) — this is plain SQL, not a business-rule enforcer.
func (IntentRepo) UpdateState(ctx context.Context, q Querier, clientOrderID string, state model.IntentState, updatedAt time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE order_intents SET state = ?, updated_at = ? WHERE client_order_id = ?`,
		state.String(), timeText(updatedAt), clientOrderID)
	if err != nil {
		return fmt.Errorf("sqlite: update order intent %s state: %w", clientOrderID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: update order intent %s state: %w", clientOrderID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns the intent with the given client order id, or ErrNotFound.
func (IntentRepo) Get(ctx context.Context, q Querier, clientOrderID string) (model.OrderIntent, error) {
	row := q.QueryRowContext(ctx, `
		SELECT client_order_id, decision_id, instrument_uid, side, qty, type, limit_price, time_in_force, state, created_at, updated_at
		FROM order_intents WHERE client_order_id = ?`, clientOrderID)
	in, err := scanIntent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.OrderIntent{}, ErrNotFound
	}
	if err != nil {
		return model.OrderIntent{}, fmt.Errorf("sqlite: get order intent %s: %w", clientOrderID, err)
	}
	return in, nil
}

// NonTerminal returns every intent not in a terminal state (filled, canceled,
// rejected), oldest first — the set the robot reconciles on startup
// (DESIGN §4).
func (IntentRepo) NonTerminal(ctx context.Context, q Querier) ([]model.OrderIntent, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT client_order_id, decision_id, instrument_uid, side, qty, type, limit_price, time_in_force, state, created_at, updated_at
		FROM order_intents
		WHERE state NOT IN (?, ?, ?)
		ORDER BY created_at ASC`,
		model.IntentFilled.String(), model.IntentCanceled.String(), model.IntentRejected.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: non-terminal order intents: %w", err)
	}
	defer rows.Close()

	var out []model.OrderIntent
	for rows.Next() {
		in, err := scanIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: non-terminal order intents: %w", err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: non-terminal order intents: %w", err)
	}
	return out, nil
}

func scanIntent(s rowScanner) (model.OrderIntent, error) {
	var in model.OrderIntent
	var uid, side, typ, tif, state, createdAt, updatedAt string
	err := s.Scan(&in.ClientOrderID, &in.DecisionID, &uid, &side, &in.Qty, &typ, &in.LimitPrice, &tif, &state, &createdAt, &updatedAt)
	if err != nil {
		return model.OrderIntent{}, err
	}
	in.InstrumentUID = model.InstrumentUID(uid)
	if in.Side, err = model.ParseSide(side); err != nil {
		return model.OrderIntent{}, err
	}
	if in.Type, err = model.ParseOrderType(typ); err != nil {
		return model.OrderIntent{}, err
	}
	if in.TimeInForce, err = model.ParseTimeInForce(tif); err != nil {
		return model.OrderIntent{}, err
	}
	if in.State, err = model.ParseIntentState(state); err != nil {
		return model.OrderIntent{}, err
	}
	if in.CreatedAt, err = parseTimeText(createdAt); err != nil {
		return model.OrderIntent{}, err
	}
	if in.UpdatedAt, err = parseTimeText(updatedAt); err != nil {
		return model.OrderIntent{}, err
	}
	return in, nil
}
