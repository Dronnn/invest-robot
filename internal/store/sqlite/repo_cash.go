package sqlite

import (
	"context"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// CashRepo persists cash movements (DESIGN §5).
type CashRepo struct{}

// Insert appends a new cash_ledger row and returns its id.
func (CashRepo) Insert(ctx context.Context, q Querier, e CashEntry) (int64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO cash_ledger (ts, delta, currency, reason, ref) VALUES (?, ?, ?, ?, ?)`,
		timeText(e.TS), e.Delta, e.Currency, e.Reason, nullString(e.Ref),
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert cash entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert cash entry: %w", err)
	}
	return id, nil
}

// Balance returns the sum of every cash_ledger delta for currency. It
// returns the zero Decimal (never an error) when there are no rows.
func (CashRepo) Balance(ctx context.Context, q Querier, currency string) (model.Decimal, error) {
	rows, err := q.QueryContext(ctx, `SELECT delta FROM cash_ledger WHERE currency = ?`, currency)
	if err != nil {
		return model.Decimal{}, fmt.Errorf("sqlite: cash balance %s: %w", currency, err)
	}
	defer rows.Close()

	var total model.Decimal
	for rows.Next() {
		var delta model.Decimal
		if err := rows.Scan(&delta); err != nil {
			return model.Decimal{}, fmt.Errorf("sqlite: cash balance %s: %w", currency, err)
		}
		total, err = total.Add(delta)
		if err != nil {
			return model.Decimal{}, fmt.Errorf("sqlite: cash balance %s: %w", currency, err)
		}
	}
	if err := rows.Err(); err != nil {
		return model.Decimal{}, fmt.Errorf("sqlite: cash balance %s: %w", currency, err)
	}
	return total, nil
}

// Recent returns up to limit cash_ledger rows, most recent first.
func (CashRepo) Recent(ctx context.Context, q Querier, limit int) ([]CashEntry, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, ts, delta, currency, reason, ref FROM cash_ledger ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent cash entries: %w", err)
	}
	defer rows.Close()

	var out []CashEntry
	for rows.Next() {
		e, err := scanCashEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: recent cash entries: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: recent cash entries: %w", err)
	}
	return out, nil
}

func scanCashEntry(s rowScanner) (CashEntry, error) {
	var e CashEntry
	var ts string
	var ref *string
	if err := s.Scan(&e.ID, &ts, &e.Delta, &e.Currency, &e.Reason, &ref); err != nil {
		return CashEntry{}, err
	}
	t, err := parseTimeText(ts)
	if err != nil {
		return CashEntry{}, err
	}
	e.TS = t
	if ref != nil {
		e.Ref = *ref
	}
	return e, nil
}
