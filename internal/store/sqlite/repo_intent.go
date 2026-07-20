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

// IllegalTransitionError reports that a requested intent state change is not a
// legal edge of the model.OrderIntent state machine (see model.CanTransition).
// It is a caller bug — the transition would never be valid regardless of the
// stored state — and in particular covers moving out of a terminal state.
type IllegalTransitionError struct {
	ClientOrderID string
	From          model.IntentState
	To            model.IntentState
}

func (e IllegalTransitionError) Error() string {
	return fmt.Sprintf("sqlite: order intent %s: illegal state transition %s -> %s", e.ClientOrderID, e.From, e.To)
}

// StateConflictError reports that a compare-and-swap UpdateState found the
// intent in a different state than the caller expected: the row exists but has
// moved under the caller (e.g. a concurrent reconciliation), so nothing was
// written. Distinct from ErrNotFound (no such intent) and from
// IllegalTransitionError (the transition is never legal).
type StateConflictError struct {
	ClientOrderID string
	Expected      model.IntentState
	Actual        model.IntentState
}

func (e StateConflictError) Error() string {
	return fmt.Sprintf("sqlite: order intent %s: state conflict, expected %s but found %s", e.ClientOrderID, e.Expected, e.Actual)
}

// UpdateState performs a guarded, compare-and-swap state change on the intent
// with the given client order id: it advances the row from `from` to `to` only
// if `from` is the currently stored state and from→to is a legal edge of the
// intent state machine (model.CanTransition). This makes the state column safe
// against lost updates — a terminal filled/canceled intent cannot silently
// regress into the reconciliation set, and two writers racing on the same
// intent cannot both "win". The reason column is left untouched.
//
// Errors: IllegalTransitionError if from→to is not a legal edge (including any
// move out of a terminal state); ErrNotFound if no such intent exists;
// StateConflictError if the intent exists but is not in `from`.
func (IntentRepo) UpdateState(ctx context.Context, q Querier, clientOrderID string, from, to model.IntentState, updatedAt time.Time) error {
	return updateIntentState(ctx, q, clientOrderID, from, to, updatedAt, nil)
}

// UpdateStateWithReason is UpdateState plus recording a human-readable reason
// for the transition in the same statement (order_intents.reason) — for
// rejected/canceled transitions where the prose belongs on the row, not just
// in the events log (DESIGN §12). reason must be non-empty; use UpdateState
// for a transition with nothing to record. Same CAS semantics and error
// types as UpdateState.
func (IntentRepo) UpdateStateWithReason(ctx context.Context, q Querier, clientOrderID string, from, to model.IntentState, updatedAt time.Time, reason string) error {
	if reason == "" {
		return fmt.Errorf("sqlite: UpdateStateWithReason: reason must not be empty (use UpdateState)")
	}
	return updateIntentState(ctx, q, clientOrderID, from, to, updatedAt, &reason)
}

// updateIntentState is the shared CAS body behind UpdateState and
// UpdateStateWithReason; reason nil means "leave the reason column alone",
// matching UpdateState's contract of touching only state and updated_at.
func updateIntentState(ctx context.Context, q Querier, clientOrderID string, from, to model.IntentState, updatedAt time.Time, reason *string) error {
	if !model.CanTransition(from, to) {
		return IllegalTransitionError{ClientOrderID: clientOrderID, From: from, To: to}
	}

	var res sql.Result
	var err error
	if reason != nil {
		res, err = q.ExecContext(ctx,
			`UPDATE order_intents SET state = ?, reason = ?, updated_at = ? WHERE client_order_id = ? AND state = ?`,
			to.String(), *reason, timeText(updatedAt), clientOrderID, from.String())
	} else {
		res, err = q.ExecContext(ctx,
			`UPDATE order_intents SET state = ?, updated_at = ? WHERE client_order_id = ? AND state = ?`,
			to.String(), timeText(updatedAt), clientOrderID, from.String())
	}
	if err != nil {
		return fmt.Errorf("sqlite: update order intent %s state: %w", clientOrderID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: update order intent %s state: %w", clientOrderID, err)
	}
	if n == 0 {
		// The CAS matched no row: either the intent is gone or its state is no
		// longer `from`. Read it back to report which, so callers can tell a
		// missing intent from a stale-state conflict.
		current, getErr := (IntentRepo{}).Get(ctx, q, clientOrderID)
		if getErr != nil {
			return getErr // ErrNotFound, or a real read error
		}
		return StateConflictError{ClientOrderID: clientOrderID, Expected: from, Actual: current.State}
	}
	return nil
}

// Get returns the intent with the given client order id, or ErrNotFound.
func (IntentRepo) Get(ctx context.Context, q Querier, clientOrderID string) (model.OrderIntent, error) {
	row := q.QueryRowContext(ctx, `
		SELECT client_order_id, decision_id, instrument_uid, side, qty, type, limit_price, time_in_force, state, reason, created_at, updated_at
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

// FindByDecision returns the intent journaled for decisionID, if any. Paper
// submission is idempotent per decision — at most one intent exists for a given
// decision_id — so this is how a retried Submit detects that a decision has
// already been journaled and avoids minting a duplicate order. found is false
// (with a nil error) when no intent references decisionID yet.
func (IntentRepo) FindByDecision(ctx context.Context, q Querier, decisionID int64) (model.OrderIntent, bool, error) {
	row := q.QueryRowContext(ctx, `
		SELECT client_order_id, decision_id, instrument_uid, side, qty, type, limit_price, time_in_force, state, reason, created_at, updated_at
		FROM order_intents WHERE decision_id = ?
		ORDER BY created_at ASC, client_order_id ASC
		LIMIT 1`, decisionID)
	in, err := scanIntent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.OrderIntent{}, false, nil
	}
	if err != nil {
		return model.OrderIntent{}, false, fmt.Errorf("sqlite: find intent by decision %d: %w", decisionID, err)
	}
	return in, true, nil
}

// NonTerminal returns every intent not in a terminal state (filled, canceled,
// rejected), oldest first — the set the robot reconciles on startup
// (DESIGN §4).
func (IntentRepo) NonTerminal(ctx context.Context, q Querier) ([]model.OrderIntent, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT client_order_id, decision_id, instrument_uid, side, qty, type, limit_price, time_in_force, state, reason, created_at, updated_at
		FROM order_intents
		WHERE state NOT IN (?, ?, ?)
		ORDER BY created_at ASC, client_order_id ASC`,
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
	var reason *string
	err := s.Scan(&in.ClientOrderID, &in.DecisionID, &uid, &side, &in.Qty, &typ, &in.LimitPrice, &tif, &state, &reason, &createdAt, &updatedAt)
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
	if reason != nil {
		in.Reason = *reason
	}
	if in.CreatedAt, err = parseTimeText(createdAt); err != nil {
		return model.OrderIntent{}, err
	}
	if in.UpdatedAt, err = parseTimeText(updatedAt); err != nil {
		return model.OrderIntent{}, err
	}
	return in, nil
}
