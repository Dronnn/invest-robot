package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Querier is satisfied by both *sql.DB and *sql.Tx (and by *DB, which embeds
// *sql.DB). Every repository method takes a Querier instead of a concrete
// type so a caller can run several repository calls inside one transaction
// via WithTx, or call them directly against the database otherwise.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	_ Querier = (*sql.DB)(nil)
	_ Querier = (*sql.Tx)(nil)
	_ Querier = (*DB)(nil)
)

// txBeginner is satisfied by *sql.DB and *DB.
type txBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// WithTx runs fn inside a transaction on db: fn returning nil commits, any
// other return rolls back and propagates that error, and a panic inside fn
// rolls back and re-panics after unwinding. This is the seam later steps use
// to apply a fill together with its position and cash-ledger mutations
// atomically.
func WithTx(ctx context.Context, db txBeginner, fn func(ctx context.Context, tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(ctx, tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("sqlite: tx failed: %w (rollback also failed: %v)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit tx: %w", err)
	}
	return nil
}
