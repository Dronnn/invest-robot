package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// ErrNotFound is returned by repository Get methods when no row matches.
var ErrNotFound = errors.New("sqlite: not found")

// InstrumentRepo persists the instrument reference cache.
type InstrumentRepo struct{}

// Upsert inserts i or, if its uid already exists, replaces every column with
// the new values (including cached_at).
func (InstrumentRepo) Upsert(ctx context.Context, q Querier, i model.Instrument, cachedAt time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO instruments (uid, figi, ticker, class_code, lot, min_price_increment, currency, name, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (uid) DO UPDATE SET
			figi = excluded.figi,
			ticker = excluded.ticker,
			class_code = excluded.class_code,
			lot = excluded.lot,
			min_price_increment = excluded.min_price_increment,
			currency = excluded.currency,
			name = excluded.name,
			cached_at = excluded.cached_at`,
		string(i.UID), string(i.FIGI), i.Ticker, i.ClassCode, i.Lot, i.MinPriceIncrement, i.Currency, i.Name, timeText(cachedAt),
	)
	if err != nil {
		return fmt.Errorf("sqlite: upsert instrument %s: %w", i.UID, err)
	}
	return nil
}

// Get returns the instrument with the given uid, or ErrNotFound.
func (InstrumentRepo) Get(ctx context.Context, q Querier, uid model.InstrumentUID) (model.Instrument, error) {
	row := q.QueryRowContext(ctx, `
		SELECT uid, figi, ticker, class_code, lot, min_price_increment, currency, name
		FROM instruments WHERE uid = ?`, string(uid))
	i, err := scanInstrument(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Instrument{}, ErrNotFound
	}
	if err != nil {
		return model.Instrument{}, fmt.Errorf("sqlite: get instrument %s: %w", uid, err)
	}
	return i, nil
}

// List returns every cached instrument, ordered by uid.
func (InstrumentRepo) List(ctx context.Context, q Querier) ([]model.Instrument, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT uid, figi, ticker, class_code, lot, min_price_increment, currency, name
		FROM instruments ORDER BY uid`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list instruments: %w", err)
	}
	defer rows.Close()

	var out []model.Instrument
	for rows.Next() {
		i, err := scanInstrument(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list instruments: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list instruments: %w", err)
	}
	return out, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanInstrument(s rowScanner) (model.Instrument, error) {
	var i model.Instrument
	var uid, figi string
	err := s.Scan(&uid, &figi, &i.Ticker, &i.ClassCode, &i.Lot, &i.MinPriceIncrement, &i.Currency, &i.Name)
	if err != nil {
		return model.Instrument{}, err
	}
	i.UID = model.InstrumentUID(uid)
	i.FIGI = model.FIGI(figi)
	return i, nil
}
