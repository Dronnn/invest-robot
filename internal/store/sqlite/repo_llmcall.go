package sqlite

import (
	"context"
	"fmt"
)

// LLMCallRepo persists every decision-engine call (DESIGN §5) so a cycle can
// be replayed exactly from its stored request/response.
type LLMCallRepo struct{}

// Insert creates a new llm_calls row and returns its id.
func (LLMCallRepo) Insert(ctx context.Context, q Querier, c LLMCall) (int64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO llm_calls (cycle_id, model, request, response, duration_ms, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.CycleID, c.Model, c.Request, nullString(c.Response), c.DurationMS, nullString(c.Error), timeText(c.CreatedAt),
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert llm_call (cycle %d): %w", c.CycleID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert llm_call (cycle %d): %w", c.CycleID, err)
	}
	return id, nil
}

// ListByCycle returns every llm_calls row for cycleID, in insertion order.
func (LLMCallRepo) ListByCycle(ctx context.Context, q Querier, cycleID int64) ([]LLMCall, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, cycle_id, model, request, response, duration_ms, error, created_at
		FROM llm_calls WHERE cycle_id = ? ORDER BY id ASC`, cycleID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list llm_calls for cycle %d: %w", cycleID, err)
	}
	defer rows.Close()

	var out []LLMCall
	for rows.Next() {
		c, err := scanLLMCall(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list llm_calls for cycle %d: %w", cycleID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list llm_calls for cycle %d: %w", cycleID, err)
	}
	return out, nil
}

func scanLLMCall(s rowScanner) (LLMCall, error) {
	var c LLMCall
	var response, errCol *string
	var createdAt string
	err := s.Scan(&c.ID, &c.CycleID, &c.Model, &c.Request, &response, &c.DurationMS, &errCol, &createdAt)
	if err != nil {
		return LLMCall{}, err
	}
	if response != nil {
		c.Response = *response
	}
	if errCol != nil {
		c.Error = *errCol
	}
	if c.CreatedAt, err = parseTimeText(createdAt); err != nil {
		return LLMCall{}, err
	}
	return c, nil
}
