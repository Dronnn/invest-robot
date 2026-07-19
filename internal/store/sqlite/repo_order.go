package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// OrderRepo persists the broker-reported view of an intent after submission
// or reconciliation (DESIGN §5): one row per client order id, upserted as
// that view changes.
type OrderRepo struct{}

// Upsert inserts o or, if its client_order_id already exists, replaces the
// broker-view columns.
func (OrderRepo) Upsert(ctx context.Context, q Querier, o Order) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO orders (client_order_id, broker_order_id, status, lots_executed, executed_price, raw_status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (client_order_id) DO UPDATE SET
			broker_order_id = excluded.broker_order_id,
			status = excluded.status,
			lots_executed = excluded.lots_executed,
			executed_price = excluded.executed_price,
			raw_status = excluded.raw_status,
			updated_at = excluded.updated_at`,
		o.ClientOrderID, nullString(o.BrokerOrderID), o.Status, o.LotsExecuted, o.ExecutedPrice, nullString(o.RawStatus), timeText(o.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("sqlite: upsert order %s: %w", o.ClientOrderID, err)
	}
	return nil
}

// Get returns the broker view for clientOrderID. ok is false if none exists.
func (OrderRepo) Get(ctx context.Context, q Querier, clientOrderID string) (o Order, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT client_order_id, broker_order_id, status, lots_executed, executed_price, raw_status, updated_at
		FROM orders WHERE client_order_id = ?`, clientOrderID)
	o, err = scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, fmt.Errorf("sqlite: get order %s: %w", clientOrderID, err)
	}
	return o, true, nil
}

func scanOrder(s rowScanner) (Order, error) {
	var o Order
	var brokerOrderID, rawStatus *string
	var updatedAt string
	err := s.Scan(&o.ClientOrderID, &brokerOrderID, &o.Status, &o.LotsExecuted, &o.ExecutedPrice, &rawStatus, &updatedAt)
	if err != nil {
		return Order{}, err
	}
	if brokerOrderID != nil {
		o.BrokerOrderID = *brokerOrderID
	}
	if rawStatus != nil {
		o.RawStatus = *rawStatus
	}
	if o.UpdatedAt, err = parseTimeText(updatedAt); err != nil {
		return Order{}, err
	}
	return o, nil
}
