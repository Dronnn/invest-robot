package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migration is one embedded, versioned schema step.
type migration struct {
	version  int
	name     string
	sql      string
	checksum string // sha256 hex of the raw file contents
}

// loadMigrations reads and orders the embedded migration files. Filenames
// must match "NNNN_description.sql" with a positive integer version; versions
// need not be contiguous but must be unique.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: read embedded migrations: %w", err)
	}

	migs := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationFilename(e.Name())
		if err != nil {
			return nil, err
		}
		if prev, ok := seen[version]; ok {
			return nil, fmt.Errorf("sqlite: duplicate migration version %d (%s and %s)", version, prev, e.Name())
		}
		seen[version] = e.Name()

		raw, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("sqlite: read migration %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		migs = append(migs, migration{
			version:  version,
			name:     name,
			sql:      string(raw),
			checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

func parseMigrationFilename(filename string) (version int, name string, err error) {
	base := strings.TrimSuffix(filename, ".sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 || parts[1] == "" {
		return 0, "", fmt.Errorf("sqlite: migration filename %q must be NNNN_description.sql", filename)
	}
	v, err := strconv.Atoi(parts[0])
	if err != nil || v <= 0 {
		return 0, "", fmt.Errorf("sqlite: migration filename %q has an invalid version prefix", filename)
	}
	return v, parts[1], nil
}

const createSchemaMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
);`

// migrate applies pending embedded migrations to db in version order, each
// inside its own transaction, and verifies the checksum of every
// already-applied migration against the embedded copy shipped in this
// binary. It refuses to proceed if the database has any applied version this
// binary does not know about — in particular a version newer than the
// highest embedded migration, which means the database was written by a
// newer build of the robot.
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createSchemaMigrationsTable); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	known := make(map[int]migration, len(migs))
	maxKnown := 0
	for _, m := range migs {
		known[m.version] = m
		if m.version > maxKnown {
			maxKnown = m.version
		}
	}

	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}

	for v, sum := range applied {
		if v > maxKnown {
			return fmt.Errorf("sqlite: database has applied migration %d, newer than the highest migration (%d) this binary knows; refusing to open", v, maxKnown)
		}
		m, ok := known[v]
		if !ok {
			return fmt.Errorf("sqlite: database has applied migration %d with no matching embedded migration file; refusing to open", v)
		}
		if m.checksum != sum {
			return fmt.Errorf("sqlite: checksum mismatch for migration %d (%s): database recorded %s, binary has %s", v, m.name, sum, m.checksum)
		}
	}

	for _, m := range migs {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]string)
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, fmt.Errorf("sqlite: scan schema_migrations: %w", err)
		}
		applied[v] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration's SQL and records it in
// schema_migrations inside a single transaction.
//
// applied_at uses time.Now() directly rather than an injected Clock: this
// package intentionally imports nothing beyond stdlib, modernc.org/sqlite,
// and internal/model, so internal/clock is not available here, and a
// migration timestamp is infrastructure bookkeeping (when this binary
// touched the schema file) rather than domain/business time subject to
// backtest replay.
func applyMigration(ctx context.Context, db *sql.DB, m migration) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin migration %d tx: %w", m.version, err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("sqlite: apply migration %d (%s): %w", m.version, m.name, err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?, ?, ?)`,
		m.version, m.checksum, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("sqlite: record migration %d: %w", m.version, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit migration %d: %w", m.version, err)
	}
	return nil
}
