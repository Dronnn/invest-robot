package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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

// LatestAsOf returns the most recent feature snapshot for uid whose as_of is
// at or before asOf — Latest's point-in-time counterpart, for the cycle
// orchestrator's assemble step to read feature state strictly at-or-before
// the decision's as_of instant with no future leakage. ok is false if none
// exists.
func (FeatureSnapshotRepo) LatestAsOf(ctx context.Context, q Querier, uid model.InstrumentUID, asOf time.Time) (snap FeatureSnapshot, ok bool, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, instrument_uid, as_of, payload FROM feature_snapshots
		WHERE instrument_uid = ? AND as_of <= ? ORDER BY as_of DESC, id DESC LIMIT 1`, string(uid), timeText(asOf))
	var iuid, snapAsOf string
	err = row.Scan(&snap.ID, &iuid, &snapAsOf, &snap.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return FeatureSnapshot{}, false, nil
	}
	if err != nil {
		return FeatureSnapshot{}, false, fmt.Errorf("sqlite: latest feature snapshot as of %s %s: %w", uid, asOf, err)
	}
	snap.InstrumentUID = model.InstrumentUID(iuid)
	snap.AsOf, err = parseTimeText(snapAsOf)
	if err != nil {
		return FeatureSnapshot{}, false, fmt.Errorf("sqlite: latest feature snapshot as of %s %s: %w", uid, asOf, err)
	}
	return snap, true, nil
}
