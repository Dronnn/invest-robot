package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver modernc.org/sqlite registers itself
// under.
const driverName = "sqlite"

// DB is the robot's single SQLite connection, configured with the pragmas
// DESIGN §5 requires (WAL, foreign keys, busy timeout) and migrated to the
// latest known schema. It embeds *sql.DB, so it satisfies Querier directly
// and callers needing a *sql.DB (e.g. for WithTx) can pass a *DB as-is.
type DB struct {
	*sql.DB
}

// Open opens (creating the file and its parent directory if necessary) the
// SQLite database at path, applies the storage-discipline pragmas, and runs
// any pending embedded migrations.
//
// Connection pool: MaxOpenConns is pinned to 1. SQLite permits only one
// writer at a time, and Phase 1's access pattern is low-frequency (one
// decision cycle at a time plus a handful of collectors) — rather than run a
// multi-connection pool and handle SQLITE_BUSY contention between readers and
// a writer, every statement is serialized through database/sql's own pool
// discipline against a single physical connection. This also sidesteps a
// real correctness hazard: journal_mode and foreign_keys are per-connection
// pragmas in SQLite (journal_mode is persisted in the file after the first
// set, but foreign_keys and busy_timeout are not), so a pool that opened
// additional connections would need every new connection to re-apply them —
// database/sql has no connection-open hook to do that. Pinning to one
// connection for the lifetime of the *DB makes "set once at Open" correct.
// Revisit (e.g. a real connect hook, or a read-only second pool) if profiling
// ever shows this serialization is a bottleneck.
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlite: create db directory %s: %w", dir, err)
		}
	}

	sqlDB, err := sql.Open(driverName, path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := applyPragmas(ctx, sqlDB); err != nil {
		sqlDB.Close()
		return nil, err
	}

	if err := migrate(ctx, sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("sqlite: migrate %s: %w", path, err)
	}

	return &DB{DB: sqlDB}, nil
}

// pragmas are applied in order on Open. synchronous=NORMAL is safe under WAL
// (only journal_mode=DELETE requires FULL for crash safety); busy_timeout is
// in milliseconds.
var pragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA foreign_keys = ON",
	"PRAGMA busy_timeout = 5000",
	"PRAGMA synchronous = NORMAL",
}

func applyPragmas(ctx context.Context, db *sql.DB) error {
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("sqlite: apply %q: %w", p, err)
		}
	}
	return nil
}
