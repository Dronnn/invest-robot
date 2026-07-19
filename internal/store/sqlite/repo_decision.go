package sqlite

import (
	"context"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// DecisionRepo persists per-instrument decisions emitted by a cycle.
type DecisionRepo struct{}

// Insert creates a new decision row and returns its id.
func (DecisionRepo) Insert(ctx context.Context, q Querier, r DecisionRecord) (int64, error) {
	var limitPrice *model.Decimal
	if r.Decision.LimitPrice != nil {
		limitPrice = r.Decision.LimitPrice
	}
	res, err := q.ExecContext(ctx, `
		INSERT INTO decisions (cycle_id, instrument_uid, action, qty, order_type, limit_price, time_in_force, rationale, confidence, raw_response, validation_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.CycleID, string(r.Decision.InstrumentUID), r.Decision.Action.String(), r.Decision.Quantity, r.Decision.OrderType.String(),
		limitPrice, r.Decision.TimeInForce.String(), r.Decision.Rationale, r.Decision.Confidence, nullString(r.RawResponse), r.ValidationStatus,
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert decision (cycle %d, %s): %w", r.CycleID, r.Decision.InstrumentUID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert decision (cycle %d, %s): %w", r.CycleID, r.Decision.InstrumentUID, err)
	}
	return id, nil
}

// ListByCycle returns every decision recorded for cycleID, in insertion
// order.
func (DecisionRepo) ListByCycle(ctx context.Context, q Querier, cycleID int64) ([]DecisionRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, cycle_id, instrument_uid, action, qty, order_type, limit_price, time_in_force, rationale, confidence, raw_response, validation_status
		FROM decisions WHERE cycle_id = ? ORDER BY id ASC`, cycleID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list decisions for cycle %d: %w", cycleID, err)
	}
	defer rows.Close()

	var out []DecisionRecord
	for rows.Next() {
		r, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list decisions for cycle %d: %w", cycleID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list decisions for cycle %d: %w", cycleID, err)
	}
	return out, nil
}

func scanDecision(s rowScanner) (DecisionRecord, error) {
	var r DecisionRecord
	var uid, action, orderType, tif string
	var limitPrice *model.Decimal
	var rawResponse *string
	err := s.Scan(&r.ID, &r.CycleID, &uid, &action, &r.Decision.Quantity, &orderType, &limitPrice, &tif,
		&r.Decision.Rationale, &r.Decision.Confidence, &rawResponse, &r.ValidationStatus)
	if err != nil {
		return DecisionRecord{}, err
	}
	r.Decision.InstrumentUID = model.InstrumentUID(uid)
	if r.Decision.Action, err = model.ParseAction(action); err != nil {
		return DecisionRecord{}, err
	}
	if r.Decision.OrderType, err = model.ParseOrderType(orderType); err != nil {
		return DecisionRecord{}, err
	}
	if r.Decision.TimeInForce, err = model.ParseTimeInForce(tif); err != nil {
		return DecisionRecord{}, err
	}
	r.Decision.LimitPrice = limitPrice
	if rawResponse != nil {
		r.RawResponse = *rawResponse
	}
	return r, nil
}

// nullString returns nil for an empty string so it stores as SQL NULL rather
// than an empty TEXT value.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
