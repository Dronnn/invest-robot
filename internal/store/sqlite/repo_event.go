package sqlite

import (
	"context"
	"fmt"
)

// EventRepo persists the structured log surfaced in the TUI/report
// (DESIGN §5).
type EventRepo struct{}

// Insert appends a new event and returns its id.
func (EventRepo) Insert(ctx context.Context, q Querier, e Event) (int64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO events (ts, level, code, payload) VALUES (?, ?, ?, ?)`,
		timeText(e.TS), e.Level, e.Code, nullString(e.Payload),
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert event: %w", err)
	}
	return id, nil
}

// Recent returns up to limit events, most recent first.
func (EventRepo) Recent(ctx context.Context, q Querier, limit int) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, ts, level, code, payload FROM events ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: recent events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: recent events: %w", err)
	}
	return out, nil
}

func scanEvent(s rowScanner) (Event, error) {
	var e Event
	var ts string
	var payload *string
	if err := s.Scan(&e.ID, &ts, &e.Level, &e.Code, &payload); err != nil {
		return Event{}, err
	}
	t, err := parseTimeText(ts)
	if err != nil {
		return Event{}, err
	}
	e.TS = t
	if payload != nil {
		e.Payload = *payload
	}
	return e, nil
}
