package sqlite

import (
	"context"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// FillRepo persists executions against order intents.
type FillRepo struct{}

// Insert appends a new fill row. lowFidelity flags a fill priced via the
// paper simulator's last-price fallback rather than a real bid/ask (DESIGN
// §7); realized_pnl starts NULL — it is set later, once known, via
// SetRealizedPnL.
func (FillRepo) Insert(ctx context.Context, q Querier, f model.Fill, lowFidelity bool) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO fills (order_intent_id, price, qty, fee, ts, low_fidelity) VALUES (?, ?, ?, ?, ?, ?)`,
		f.IntentID, f.Price, f.Qty, f.Fee, timeText(f.TS), lowFidelity,
	)
	if err != nil {
		return fmt.Errorf("sqlite: insert fill for intent %s: %w", f.IntentID, err)
	}
	return nil
}

// SetRealizedPnL sets the realized_pnl column on the fill recorded against
// intentID. Phase 1 assumes full fills only (DESIGN §7), so an intent has at
// most one fill row and this UPDATE is unambiguous. ErrNotFound if no fill
// row exists for intentID.
func (FillRepo) SetRealizedPnL(ctx context.Context, q Querier, intentID string, pnl model.Decimal) error {
	res, err := q.ExecContext(ctx, `UPDATE fills SET realized_pnl = ? WHERE order_intent_id = ?`, pnl, intentID)
	if err != nil {
		return fmt.Errorf("sqlite: set realized pnl for intent %s: %w", intentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: set realized pnl for intent %s: %w", intentID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListByIntent returns every fill recorded against intentID, oldest first.
func (FillRepo) ListByIntent(ctx context.Context, q Querier, intentID string) ([]FillRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT order_intent_id, price, qty, fee, ts, realized_pnl, low_fidelity FROM fills
		WHERE order_intent_id = ? ORDER BY ts ASC, id ASC`, intentID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list fills for intent %s: %w", intentID, err)
	}
	defer rows.Close()

	var out []FillRecord
	for rows.Next() {
		f, err := scanFillRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list fills for intent %s: %w", intentID, err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list fills for intent %s: %w", intentID, err)
	}
	return out, nil
}

// Recent returns up to limit fill rows, most recent first. A negative limit
// returns every row — SQLite's documented "a negative LIMIT means no upper
// bound" — which is how portfolio.DayPnL walks every fill since a session
// boundary: FillRepo has no time-ranged read of its own, mirroring
// CashRepo.Recent's shape and the same reason for it (this project's scale
// doesn't warrant one yet).
func (FillRepo) Recent(ctx context.Context, q Querier, limit int) ([]FillRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT order_intent_id, price, qty, fee, ts, realized_pnl, low_fidelity FROM fills
		ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent fills: %w", err)
	}
	defer rows.Close()

	var out []FillRecord
	for rows.Next() {
		f, err := scanFillRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: recent fills: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: recent fills: %w", err)
	}
	return out, nil
}

func scanFillRecord(s rowScanner) (FillRecord, error) {
	var f FillRecord
	var ts string
	var lowFidelity int
	if err := s.Scan(&f.IntentID, &f.Price, &f.Qty, &f.Fee, &ts, &f.RealizedPnL, &lowFidelity); err != nil {
		return FillRecord{}, err
	}
	t, err := parseTimeText(ts)
	if err != nil {
		return FillRecord{}, err
	}
	f.TS = t
	f.LowFidelity = lowFidelity != 0
	return f, nil
}
