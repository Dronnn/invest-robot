package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// FeatureSnapshotRepo persists computed indicator snapshots. History is
// append-only.
type FeatureSnapshotRepo struct{}

// Insert appends a new feature snapshot and returns its id.
func (FeatureSnapshotRepo) Insert(ctx context.Context, q Querier, s FeatureSnapshot) (int64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO feature_snapshots (instrument_uid, as_of, payload) VALUES (?, ?, ?)`,
		string(s.InstrumentUID), timeText(s.AsOf), s.Payload,
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert feature snapshot %s: %w", s.InstrumentUID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert feature snapshot %s: %w", s.InstrumentUID, err)
	}
	return id, nil
}

// Latest returns the most recent feature snapshot for uid. ok is false if
// none exists.
func (FeatureSnapshotRepo) Latest(ctx context.Context, q Querier, uid model.InstrumentUID) (snap FeatureSnapshot, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, instrument_uid, as_of, payload FROM feature_snapshots
		WHERE instrument_uid = ? ORDER BY as_of DESC, id DESC LIMIT 1`, string(uid))
	var iuid, asOf string
	err = row.Scan(&snap.ID, &iuid, &asOf, &snap.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return FeatureSnapshot{}, false, nil
	}
	if err != nil {
		return FeatureSnapshot{}, false, fmt.Errorf("sqlite: latest feature snapshot %s: %w", uid, err)
	}
	snap.InstrumentUID = model.InstrumentUID(iuid)
	snap.AsOf, err = parseTimeText(asOf)
	if err != nil {
		return FeatureSnapshot{}, false, fmt.Errorf("sqlite: latest feature snapshot %s: %w", uid, err)
	}
	return snap, true, nil
}
