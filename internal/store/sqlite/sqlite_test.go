package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "robot.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpen_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", "robot.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
}

func TestOpen_Pragmas(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}

	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("query synchronous: %v", err)
	}
	if synchronous != 1 { // NORMAL == 1
		t.Errorf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}
}

func TestOpen_ForeignKeysEnforced(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO candles (instrument_uid, interval, open, high, low, close, volume, ts, complete)
		VALUES ('missing-uid', '1m', '1', '1', '1', '1', 1, ?, 1)`, timeText(time.Now()))
	if err == nil {
		t.Fatal("expected foreign key violation inserting a candle for an unknown instrument, got nil error")
	}
}

func TestMigrate_AppliesFromEmpty(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if version != 1 {
		t.Errorf("max applied version = %d, want 1", version)
	}

	tables := []string{
		"instruments", "candles", "quotes", "feature_snapshots", "cycles",
		"decisions", "llm_calls", "order_intents", "orders", "fills",
		"positions", "cash_ledger", "equity_snapshots", "events",
	}
	for _, name := range tables {
		var got string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
		if err != nil {
			t.Errorf("table %s missing after migration: %v", name, err)
		}
	}
}

func TestMigrate_IdempotentReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "robot.db")
	ctx := context.Background()

	db1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations row count = %d, want 1 (reapplying must be a no-op)", count)
	}
}

func TestMigrate_ChecksumMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "robot.db")
	ctx := context.Background()

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1`); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	db.Close()

	_, err = Open(ctx, path)
	if err == nil {
		t.Fatal("expected checksum-mismatch error reopening a tampered database, got nil")
	}
}

func TestMigrate_RefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "robot.db")
	ctx := context.Background()

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (999, 'future', ?)`, timeText(time.Now())); err != nil {
		t.Fatalf("insert fake future version: %v", err)
	}
	db.Close()

	_, err = Open(ctx, path)
	if err == nil {
		t.Fatal("expected refusal to open a database with a migration version newer than known, got nil")
	}
}

func TestLoadMigrations_SortedAndChecksummed(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no migrations loaded")
	}
	for i := 1; i < len(migs); i++ {
		if migs[i-1].version >= migs[i].version {
			t.Errorf("migrations not strictly sorted at index %d: %d >= %d", i, migs[i-1].version, migs[i].version)
		}
	}
	for _, m := range migs {
		if m.checksum == "" {
			t.Errorf("migration %d has empty checksum", m.version)
		}
	}
}

func TestParseMigrationFilename(t *testing.T) {
	cases := []struct {
		filename    string
		wantVersion int
		wantName    string
		wantErr     bool
	}{
		{"0001_init.sql", 1, "init", false},
		{"0042_add_news.sql", 42, "add_news", false},
		{"init.sql", 0, "", true},
		{"0001.sql", 0, "", true},
		{"abcd_init.sql", 0, "", true},
		{"0000_init.sql", 0, "", true},
	}
	for _, c := range cases {
		v, name, err := parseMigrationFilename(c.filename)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMigrationFilename(%q): want error, got nil", c.filename)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMigrationFilename(%q): unexpected error: %v", c.filename, err)
			continue
		}
		if v != c.wantVersion || name != c.wantName {
			t.Errorf("parseMigrationFilename(%q) = (%d, %q), want (%d, %q)", c.filename, v, name, c.wantVersion, c.wantName)
		}
	}
}

func TestWithTx_CommitsOnSuccess(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seedInstrument(t, db, "uid-1")

	err := WithTx(ctx, db.DB, func(ctx context.Context, tx *sql.Tx) error {
		return (InstrumentRepo{}).Upsert(ctx, tx, testInstrument("uid-2"), time.Now())
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	list, err := (InstrumentRepo{}).List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len(list) = %d, want 2 after committed tx", len(list))
	}
}

func TestWithTx_RollsBackOnError(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seedInstrument(t, db, "uid-1")

	sentinel := fmt.Errorf("boom")
	err := WithTx(ctx, db.DB, func(ctx context.Context, tx *sql.Tx) error {
		if err := (InstrumentRepo{}).Upsert(ctx, tx, testInstrument("uid-2"), time.Now()); err != nil {
			return err
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("WithTx error = %v, want sentinel", err)
	}

	list, err := (InstrumentRepo{}).List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len(list) = %d, want 1 (rolled back write must not be visible)", len(list))
	}
}

func TestWithTx_RollsBackOnPanic(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seedInstrument(t, db, "uid-1")

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected WithTx to re-panic")
			}
		}()
		_ = WithTx(ctx, db.DB, func(ctx context.Context, tx *sql.Tx) error {
			if err := (InstrumentRepo{}).Upsert(ctx, tx, testInstrument("uid-2"), time.Now()); err != nil {
				t.Fatal(err)
			}
			panic("boom")
		})
	}()

	list, err := (InstrumentRepo{}).List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len(list) = %d, want 1 (panicked tx must not commit)", len(list))
	}
}

// TestParallelReads_Smoke exercises the pinned single-connection pool under
// -race: concurrent readers must not corrupt shared state even though every
// statement is serialized onto one physical connection.
func TestParallelReads_Smoke(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		seedInstrument(t, db, fmt.Sprintf("uid-%d", i))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := (InstrumentRepo{}).List(ctx, db); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent List: %v", err)
	}
}
