package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// QuoteRepo persists top-of-book snapshots. History is append-only; there is
// no upsert.
type QuoteRepo struct{}

// Insert appends a new quote snapshot.
func (QuoteRepo) Insert(ctx context.Context, q Querier, quote model.Quote) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO quotes (instrument_uid, bid, ask, last, ts) VALUES (?, ?, ?, ?, ?)`,
		string(quote.InstrumentUID), quote.Bid, quote.Ask, quote.Last, timeText(quote.TS),
	)
	if err != nil {
		return fmt.Errorf("sqlite: insert quote %s: %w", quote.InstrumentUID, err)
	}
	return nil
}

// Latest returns the most recent quote for uid. ok is false if none exists.
func (QuoteRepo) Latest(ctx context.Context, q Querier, uid model.InstrumentUID) (quote model.Quote, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT instrument_uid, bid, ask, last, ts FROM quotes
		WHERE instrument_uid = ? ORDER BY ts DESC LIMIT 1`, string(uid))
	quote, err = scanQuote(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Quote{}, false, nil
	}
	if err != nil {
		return model.Quote{}, false, fmt.Errorf("sqlite: latest quote %s: %w", uid, err)
	}
	return quote, true, nil
}

func scanQuote(s rowScanner) (model.Quote, error) {
	var quote model.Quote
	var uid, ts string
	if err := s.Scan(&uid, &quote.Bid, &quote.Ask, &quote.Last, &ts); err != nil {
		return model.Quote{}, err
	}
	quote.InstrumentUID = model.InstrumentUID(uid)
	t, err := parseTimeText(ts)
	if err != nil {
		return model.Quote{}, err
	}
	quote.TS = t
	return quote, nil
}
