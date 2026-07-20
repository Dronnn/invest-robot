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

// legacyV1Checksum is the sha256 of migration 1's SQL as it existed between
// commits f350897 and 1021436, before 0001_init.sql was restored to its
// originally published shape. For that window, order_intents.reason,
// fills.realized_pnl and fills.low_fidelity were folded directly into v1;
// they now live in migration 2 instead. A database created against that
// intermediate schema recorded this checksum for version 1 and would
// otherwise fail the checksum check below and refuse to open. The runner
// accepts it as an equivalent legacy variant of version 1 and, when it sees
// this checksum, runs legacyV1UpgradeTo2SQL for migration 2 instead of 2's
// canonical SQL — the three columns already exist there, so only the unique
// index is still missing. Either path records migration 2's canonical
// checksum, so the two upgrade routes converge to the same recorded state.
const legacyV1Checksum = "14a25973da9a9f34dec118f569401835018489871ca5eb49d190876979775d05"

// legacyV1UpgradeTo2SQL is what migration 2 runs against a legacyV1Checksum
// database in place of its canonical SQL (see legacyV1Checksum).
const legacyV1UpgradeTo2SQL = `CREATE UNIQUE INDEX idx_fills_order_intent_unique ON fills (order_intent_id);`

// migrationPrecondition validates data-dependent invariants immediately
// before a specific pending migration is applied — checks too dependent on
// existing row contents to express as schema DDL or a CHECK constraint.
// Returning an error aborts startup before the migration's DDL runs; the
// caller rolls back the transaction the precondition ran in.
type migrationPrecondition func(ctx context.Context, tx *sql.Tx) error

var migrationPreconditions = map[int]migrationPrecondition{
	2: checkNoDuplicateFillsPerIntent,
}

// checkNoDuplicateFillsPerIntent guards migration 2's
// UNIQUE(order_intent_id) index on fills. A published-v1 database (canonical
// or the legacyV1Checksum variant) that somehow accumulated more than one
// fill row for the same order intent would otherwise abort partway through
// migration 2 with an opaque SQLite constraint-violation error. Fail fast
// instead, naming the offending intents and the remedy. There is no
// automatic repair: deciding which fill row is authoritative for an intent
// is not the migration runner's call to make.
//
// Version 2 is this package's version number, but migrationPreconditions is
// keyed on it alone, and the atomic-rollback/upgrade-only-pending tests drive
// applyMigrations with synthetic, unrelated schemas that happen to reuse
// version 2. Checking for the fills table first keeps this precondition a
// no-op against any schema that isn't actually the one it guards, in tests
// and in any future reuse of applyMigrations with a different migration set.
func checkNoDuplicateFillsPerIntent(ctx context.Context, tx *sql.Tx) error {
	var haveFillsTable int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'fills'`,
	).Scan(&haveFillsTable); err != nil {
		return fmt.Errorf("sqlite: check duplicate fills before migration 2: %w", err)
	}
	if haveFillsTable == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT order_intent_id FROM fills
		GROUP BY order_intent_id HAVING COUNT(*) > 1
		ORDER BY order_intent_id`)
	if err != nil {
		return fmt.Errorf("sqlite: check duplicate fills before migration 2: %w", err)
	}
	defer rows.Close()

	var dupes []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("sqlite: check duplicate fills before migration 2: %w", err)
		}
		dupes = append(dupes, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: check duplicate fills before migration 2: %w", err)
	}
	if len(dupes) == 0 {
		return nil
	}
	return fmt.Errorf(
		"sqlite: cannot apply migration 2: order_intent_id(s) %s have more than one fill row, which the new UNIQUE(order_intent_id) index forbids; "+
			"for a paper-mode database it is safe to delete the file and let it recreate itself on next start, otherwise remove the duplicate fill rows for these intents by hand before restarting",
		strings.Join(dupes, ", "),
	)
}

// migrate loads the embedded migrations and applies any that are pending to
// db. now supplies the applied_at timestamp; the caller injects it (Open
// passes time.Now().UTC) so no time source is read below the orchestration
// layer (DESIGN §3's no-time.Now rule).
func migrate(ctx context.Context, db *sql.DB, now func() time.Time) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	return applyMigrations(ctx, db, migs, now)
}

// applyMigrations applies the given migrations to db in version order, each
// inside its own transaction, and verifies the checksum of every
// already-applied migration against the copy in migs (accepting
// legacyV1Checksum as an equivalent variant of version 1). It refuses to
// proceed if the database has any applied version not present in migs — in
// particular a version newer than the highest given migration, which means
// the database was written by a newer build of the robot. Taking the
// migration set as a parameter (rather than always loading the embedded one)
// lets the upgrade and atomic-rollback tests drive it with synthetic
// migrations.
func applyMigrations(ctx context.Context, db *sql.DB, migs []migration, now func() time.Time) error {
	if _, err := db.ExecContext(ctx, createSchemaMigrationsTable); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
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

	legacyV1 := false
	for v, sum := range applied {
		if v > maxKnown {
			return fmt.Errorf("sqlite: database has applied migration %d, newer than the highest migration (%d) this binary knows; refusing to open", v, maxKnown)
		}
		m, ok := known[v]
		if !ok {
			return fmt.Errorf("sqlite: database has applied migration %d with no matching embedded migration file; refusing to open", v)
		}
		if m.checksum == sum {
			continue
		}
		if v == 1 && sum == legacyV1Checksum {
			legacyV1 = true
			continue
		}
		return fmt.Errorf("sqlite: checksum mismatch for migration %d (%s): database recorded %s, binary has %s", v, m.name, sum, m.checksum)
	}

	for _, m := range migs {
		if _, ok := applied[m.version]; ok {
			continue
		}
		sqlText := m.sql
		if m.version == 2 && legacyV1 {
			sqlText = legacyV1UpgradeTo2SQL
		}
		if err := applyMigration(ctx, db, m, sqlText, now); err != nil {
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

// applyMigration runs sqlText — m's canonical SQL, or a legacy-compatibility
// substitute the caller chose for m.version — and records m's canonical
// checksum in schema_migrations, inside a single transaction, so the DDL and
// its schema_migrations row commit or roll back atomically: a migration
// whose SQL fails partway leaves neither the partial schema change nor a
// version row behind. Any migrationPreconditions entry for m.version runs
// first, inside the same transaction, and can abort before the DDL runs.
// applied_at comes from the injected now func (DESIGN §3's no-time.Now rule)
// and is stored in the same fixed-width UTC form as every other timestamp
// column.
func applyMigration(ctx context.Context, db *sql.DB, m migration, sqlText string, now func() time.Time) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin migration %d tx: %w", m.version, err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if check, ok := migrationPreconditions[m.version]; ok {
		if err = check(ctx, tx); err != nil {
			return err
		}
	}

	if _, err = tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("sqlite: apply migration %d (%s): %w", m.version, m.name, err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?, ?, ?)`,
		m.version, m.checksum, timeText(now()),
	); err != nil {
		return fmt.Errorf("sqlite: record migration %d: %w", m.version, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit migration %d: %w", m.version, err)
	}
	return nil
}
