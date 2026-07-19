package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

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
// Pragmas are carried on the DSN (_pragma=… query parameters) rather than
// executed once after opening. journal_mode, foreign_keys, busy_timeout and
// synchronous are per-connection settings in SQLite (only journal_mode's WAL
// switch persists in the file); the modernc.org/sqlite driver re-runs every
// DSN _pragma on each new physical connection it opens, so a connection the
// database/sql pool silently discards and replaces still comes up with foreign
// keys on and the busy timeout set. Setting them once on the first connection
// (the previous approach) left any replacement connection running with
// foreign_keys=OFF.
//
// MaxOpenConns is still pinned to 1: SQLite permits a single writer, and Phase
// 1's access pattern is low-frequency, so serializing every statement onto one
// physical connection sidesteps SQLITE_BUSY contention entirely. The DSN
// pragmas make that pin a defense-in-depth measure for correctness rather than
// the sole thing keeping the pragmas applied. Revisit (e.g. a read-only second
// pool) only if profiling ever shows the serialization is a bottleneck.
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlite: create db directory %s: %w", dir, err)
		}
	}

	sqlDB, err := sql.Open(driverName, pragmaDSN(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := migrate(ctx, sqlDB, func() time.Time { return time.Now().UTC() }); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("sqlite: migrate %s: %w", path, err)
	}

	return &DB{DB: sqlDB}, nil
}

// connPragmas are applied to every physical connection via the DSN. Order is
// irrelevant — the driver sorts busy_timeout first regardless. synchronous=
// NORMAL is safe under WAL (only journal_mode=DELETE requires FULL for crash
// safety); busy_timeout is in milliseconds.
var connPragmas = []string{
	"journal_mode(WAL)",
	"foreign_keys(1)",
	"busy_timeout(5000)",
	"synchronous(NORMAL)",
}

// pragmaDSN builds the modernc.org/sqlite connection string for path with the
// storage-discipline pragmas attached as _pragma query parameters. path is a
// plain filename (not a file: URI), so the driver strips the query before
// opening the file but still applies every _pragma per connection.
func pragmaDSN(path string) string {
	q := url.Values{}
	for _, p := range connPragmas {
		q.Add("_pragma", p)
	}
	return path + "?" + q.Encode()
}
