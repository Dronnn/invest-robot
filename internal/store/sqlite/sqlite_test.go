package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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

// TestOpen_PragmasSurviveConnectionReplacement forces database/sql to discard
// and reopen the underlying physical connection between statements (a 1ns
// connection lifetime expires it immediately) and re-checks the pragmas. Only
// pragmas carried on the DSN survive that; the pre-fix "set once on the first
// connection" approach left a replacement connection with foreign_keys OFF and
// no busy timeout, which these checks would catch.
func TestOpen_PragmasSurviveConnectionReplacement(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	db.SetConnMaxLifetime(time.Nanosecond)

	// Cycle the pool a few times so the physical connection is definitely a
	// replacement, not the one Open created.
	for i := 0; i < 5; i++ {
		if _, err := db.ExecContext(ctx, "SELECT 1"); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys after connection replacement = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout after connection replacement = %d, want 5000", busyTimeout)
	}

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode after connection replacement = %q, want wal", journalMode)
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
	if want := wantLatestMigrationVersion(t); version != want {
		t.Errorf("max applied version = %d, want %d", version, want)
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
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if want := len(migs); count != want {
		t.Errorf("schema_migrations row count = %d, want %d (reapplying must be a no-op)", count, want)
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

// fixedNow is an injected migration clock for the migration tests.
func fixedNow() time.Time { return time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC) }

// wantLatestMigrationVersion returns the highest version among the embedded
// migrations, so tests assert against the schema as it evolves instead of a
// hardcoded number that has to be bumped by hand for every new migration
// file.
func wantLatestMigrationVersion(t *testing.T) int {
	t.Helper()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no embedded migrations found")
	}
	return migs[len(migs)-1].version
}

// rawTestDB opens a database without running the embedded migrations, so a test
// can drive applyMigrations with its own synthetic migration set.
func rawTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raw.db")
	db, err := sql.Open(driverName, pragmaDSN(path))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// testMigration builds a migration with a checksum computed the same way the
// loader does, so an already-applied synthetic migration passes the checksum
// verification on a later applyMigrations call.
func testMigration(version int, name, sqlText string) migration {
	sum := sha256.Sum256([]byte(sqlText))
	return migration{version: version, name: name, sql: sqlText, checksum: hex.EncodeToString(sum[:])}
}

// TestApplyMigrations_UpgradeFromPriorVersionFixture opens a committed
// database file that predates the migration system (version 0, carrying a
// legacy marker row) and upgrades it in place through Open. It proves every
// pending migration applies to an existing file and does not disturb data
// already there — the design-required prior-version upgrade path.
func TestApplyMigrations_UpgradeFromPriorVersionFixture(t *testing.T) {
	ctx := context.Background()

	// Open writes to the file, so work on a copy of the committed fixture.
	src := filepath.Join("testdata", "migrate", "prior_v0.db")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "prior_v0.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open (upgrade prior-version fixture): %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if want := wantLatestMigrationVersion(t); version != want {
		t.Errorf("version after upgrade = %d, want %d", version, want)
	}

	// The pending migration's tables now exist.
	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='candles'`).Scan(&name); err != nil {
		t.Errorf("candles table missing after upgrade: %v", err)
	}

	// The pre-existing legacy data survived the in-place upgrade.
	var note string
	if err := db.QueryRowContext(ctx, `SELECT note FROM legacy_marker`).Scan(&note); err != nil {
		t.Fatalf("legacy_marker row missing after upgrade: %v", err)
	}
	if note != "pre-migration-v0" {
		t.Errorf("legacy_marker note = %q, want pre-migration-v0", note)
	}
}

// TestApplyMigrations_UpgradeFromRestoredV1 builds a database with only the
// restored, published v1 schema applied (no reason/realized_pnl/low_fidelity
// columns — those were briefly and wrongly folded into 0001_init.sql before
// release, then split out into migration 2) and seeds it with data in that
// original shape. It then proves the full embedded migration set upgrades it
// cleanly: v2's columns exist and are writable, the new
// one-fill-per-intent unique index is enforced, and the pre-existing rows
// survive untouched. This is the exact shape any database created before v2
// shipped will be in the next time it opens.
func TestApplyMigrations_UpgradeFromRestoredV1(t *testing.T) {
	ctx := context.Background()
	db := rawTestDB(t)

	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	var v1 migration
	haveV1 := false
	for _, m := range migs {
		if m.version == 1 {
			v1, haveV1 = m, true
			break
		}
	}
	if !haveV1 {
		t.Fatal("migration version 1 not found among embedded migrations")
	}

	if err := applyMigrations(ctx, db, []migration{v1}, fixedNow); err != nil {
		t.Fatalf("apply restored v1 alone: %v", err)
	}

	// Seed data in the original v1 shape — no reason/realized_pnl/low_fidelity
	// columns exist yet at this point.
	now := timeText(fixedNow())
	if _, err := db.ExecContext(ctx, `INSERT INTO instruments (uid, figi, ticker, class_code, lot, min_price_increment, currency, name, cached_at)
		VALUES ('uid-1', 'figi-1', 'TICK', 'TQBR', 10, '0.01', 'RUB', 'Test', ?)`, now); err != nil {
		t.Fatalf("seed instrument: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO cycles (started_at, as_of, mode, engine, engine_version, prompt_template_hash, config_snapshot, status)
		VALUES (?, ?, 'paper', 'rules', 'v1', 'hash', '{}', 'done')`, now, now); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO decisions (cycle_id, instrument_uid, action, qty, order_type, time_in_force, rationale, confidence, validation_status)
		VALUES (1, 'uid-1', 'buy', 1, 'market', 'day', 'because', 1.0, 'ok')`); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO order_intents (client_order_id, decision_id, instrument_uid, side, qty, type, time_in_force, state, created_at, updated_at)
		VALUES ('intent-1', 1, 'uid-1', 'buy', 1, 'market', 'day', 'filled', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed order intent in the original v1 shape (no reason column): %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fills (order_intent_id, price, qty, fee, ts) VALUES ('intent-1', '100', 1, '0.1', ?)`, now); err != nil {
		t.Fatalf("seed fill in the original v1 shape (no realized_pnl/low_fidelity columns): %v", err)
	}

	// Upgrade in place with the full embedded migration set (v1 already
	// applied and unchanged, v2 pending).
	if err := applyMigrations(ctx, db, migs, fixedNow); err != nil {
		t.Fatalf("upgrade restored v1 to latest: %v", err)
	}

	var maxVersion int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&maxVersion); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if want := migs[len(migs)-1].version; maxVersion != want {
		t.Errorf("version after upgrade = %d, want %d", maxVersion, want)
	}

	// The pre-existing rows survived untouched.
	var side, state string
	if err := db.QueryRowContext(ctx, `SELECT side, state FROM order_intents WHERE client_order_id = 'intent-1'`).Scan(&side, &state); err != nil {
		t.Fatalf("read order intent after upgrade: %v", err)
	}
	if side != "buy" || state != "filled" {
		t.Errorf("order intent after upgrade = {side:%s state:%s}, want {buy filled}", side, state)
	}

	var price string
	var lowFidelity int
	var realizedPnL sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT price, low_fidelity, realized_pnl FROM fills WHERE order_intent_id = 'intent-1'`).
		Scan(&price, &lowFidelity, &realizedPnL); err != nil {
		t.Fatalf("read fill after upgrade: %v", err)
	}
	if price != "100" {
		t.Errorf("fill price after upgrade = %q, want 100 (pre-existing data must survive)", price)
	}
	if lowFidelity != 0 {
		t.Errorf("fill low_fidelity after upgrade = %d, want 0 (v2's DEFAULT backfilling a pre-existing row)", lowFidelity)
	}
	if realizedPnL.Valid {
		t.Errorf("fill realized_pnl after upgrade = %v, want NULL for a pre-existing row", realizedPnL)
	}

	// v2's new columns are usable going forward.
	if _, err := db.ExecContext(ctx, `UPDATE order_intents SET reason = 'because' WHERE client_order_id = 'intent-1'`); err != nil {
		t.Errorf("write order_intents.reason after upgrade: %v", err)
	}

	// The new unique index enforces one fill per intent.
	if _, err := db.ExecContext(ctx, `INSERT INTO fills (order_intent_id, price, qty, fee, ts) VALUES ('intent-1', '101', 1, '0.1', ?)`, now); err == nil {
		t.Error("expected a second fill for the same intent to violate the new UNIQUE(order_intent_id) index, got nil error")
	}
}

// TestApplyMigrations_UpgradeFromLegacyV1Checksum builds a database whose
// version 1 was applied against the amended-v1 schema (order_intents.reason,
// fills.realized_pnl and fills.low_fidelity already inline — the shape that
// shipped briefly between commits f350897 and 1021436, recording
// legacyV1Checksum) and proves the real embedded migrations upgrade it
// cleanly: migration 2 runs its legacy substitute SQL (only the unique
// index; the three columns already exist), the recorded version-2 checksum
// is the canonical one so a later reopen looks identical to the clean path,
// pre-existing data survives, the unique index is enforced, and the
// resulting order_intents/fills schema matches a fresh clean-path database
// column-for-column and index-for-index.
func TestApplyMigrations_UpgradeFromLegacyV1Checksum(t *testing.T) {
	ctx := context.Background()

	amendedV1SQL, err := os.ReadFile(filepath.Join("testdata", "migrate", "legacy_v1_amended.sql"))
	if err != nil {
		t.Fatalf("read legacy v1 fixture: %v", err)
	}
	legacyV1 := testMigration(1, "init", string(amendedV1SQL))
	if legacyV1.checksum != legacyV1Checksum {
		t.Fatalf("legacy_v1_amended.sql checksum = %s, want legacyV1Checksum %s (migrate.go's constant and the fixture have drifted apart)",
			legacyV1.checksum, legacyV1Checksum)
	}

	db := rawTestDB(t)
	if _, err := db.ExecContext(ctx, createSchemaMigrationsTable); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, legacyV1.sql); err != nil {
		t.Fatalf("apply amended v1 schema: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (1, ?, ?)`,
		legacyV1Checksum, timeText(fixedNow())); err != nil {
		t.Fatalf("record legacy v1 as applied: %v", err)
	}

	// Seed data in the amended-v1 shape: reason/realized_pnl/low_fidelity are
	// already real columns here (unlike the restored-v1 fixture above), so
	// give them values to prove they survive the upgrade untouched too.
	now := timeText(fixedNow())
	if _, err := db.ExecContext(ctx, `INSERT INTO instruments (uid, figi, ticker, class_code, lot, min_price_increment, currency, name, cached_at)
		VALUES ('uid-1', 'figi-1', 'TICK', 'TQBR', 10, '0.01', 'RUB', 'Test', ?)`, now); err != nil {
		t.Fatalf("seed instrument: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO cycles (started_at, as_of, mode, engine, engine_version, prompt_template_hash, config_snapshot, status)
		VALUES (?, ?, 'paper', 'rules', 'v1', 'hash', '{}', 'done')`, now, now); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO decisions (cycle_id, instrument_uid, action, qty, order_type, time_in_force, rationale, confidence, validation_status)
		VALUES (1, 'uid-1', 'buy', 1, 'market', 'day', 'because', 1.0, 'ok')`); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO order_intents (client_order_id, decision_id, instrument_uid, side, qty, type, time_in_force, state, reason, created_at, updated_at)
		VALUES ('intent-1', 1, 'uid-1', 'buy', 1, 'market', 'day', 'filled', 'because', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed order intent (amended v1 shape, reason already a real column): %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO fills (order_intent_id, price, qty, fee, ts, realized_pnl, low_fidelity) VALUES ('intent-1', '100', 1, '0.1', ?, '5', 1)`, now); err != nil {
		t.Fatalf("seed fill (amended v1 shape, realized_pnl/low_fidelity already real columns): %v", err)
	}

	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if err := applyMigrations(ctx, db, migs, fixedNow); err != nil {
		t.Fatalf("upgrade legacy-checksum v1 to latest: %v", err)
	}

	var maxVersion int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&maxVersion); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if want := migs[len(migs)-1].version; maxVersion != want {
		t.Errorf("version after upgrade = %d, want %d", maxVersion, want)
	}

	var v2Checksum string
	if err := db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version = 2`).Scan(&v2Checksum); err != nil {
		t.Fatalf("read recorded v2 checksum: %v", err)
	}
	var canonicalV2 migration
	for _, m := range migs {
		if m.version == 2 {
			canonicalV2 = m
		}
	}
	if v2Checksum != canonicalV2.checksum {
		t.Errorf("recorded v2 checksum = %s, want the canonical migration 2 checksum %s (so a later reopen matches the clean path)", v2Checksum, canonicalV2.checksum)
	}

	// Pre-existing rows, including the columns the amended v1 already had,
	// survived the upgrade untouched.
	var reason, price string
	var lowFidelity int
	var realizedPnL string
	if err := db.QueryRowContext(ctx, `SELECT reason FROM order_intents WHERE client_order_id = 'intent-1'`).Scan(&reason); err != nil {
		t.Fatalf("read order intent after upgrade: %v", err)
	}
	if reason != "because" {
		t.Errorf("order_intents.reason after upgrade = %q, want because", reason)
	}
	if err := db.QueryRowContext(ctx, `SELECT price, low_fidelity, realized_pnl FROM fills WHERE order_intent_id = 'intent-1'`).
		Scan(&price, &lowFidelity, &realizedPnL); err != nil {
		t.Fatalf("read fill after upgrade: %v", err)
	}
	if price != "100" || lowFidelity != 1 || realizedPnL != "5" {
		t.Errorf("fill after upgrade = {price:%s low_fidelity:%d realized_pnl:%s}, want {100 1 5}", price, lowFidelity, realizedPnL)
	}

	// The unique index exists and is enforced, same as the clean path.
	if _, err := db.ExecContext(ctx, `INSERT INTO fills (order_intent_id, price, qty, fee, ts) VALUES ('intent-1', '101', 1, '0.1', ?)`, now); err == nil {
		t.Error("expected a second fill for the same intent to violate the unique index, got nil error")
	}

	// The legacy-upgrade path converges to the same schema shape as a clean
	// Open() through the canonical migrations, column-for-column and
	// index-for-index. Comparing via PRAGMA table_info/index_info rather than
	// sqlite_master text: ALTER TABLE ADD COLUMN does not necessarily leave
	// sqlite_master.sql byte-identical to a table whose column was authored
	// inline, even though the resulting schema is equivalent.
	clean := openTest(t)
	for _, table := range []string{"order_intents", "fills"} {
		gotCols, wantCols := columnInfoRows(t, db, table), columnInfoRows(t, clean.DB, table)
		if !slices.Equal(gotCols, wantCols) {
			t.Errorf("%s columns after legacy upgrade = %v, want %v (clean-path shape)", table, gotCols, wantCols)
		}
		gotIdx, wantIdx := indexInfoRows(t, db, table), indexInfoRows(t, clean.DB, table)
		if !slices.Equal(gotIdx, wantIdx) {
			t.Errorf("%s indexes after legacy upgrade = %v, want %v (clean-path shape)", table, gotIdx, wantIdx)
		}
	}
}

// columnInfoRows returns table's columns (name, declared type, notnull,
// default, pk) via PRAGMA table_info, normalized into comparable strings and
// name-sorted. Sorted, not positional: ALTER TABLE ADD COLUMN always appends
// the new column at the end, so the legacy-checksum upgrade path (reason
// authored inline in CREATE TABLE) and the clean path (reason added via
// ALTER TABLE) never share physical column order even when the column set is
// identical — cosmetic position isn't the invariant under test here.
func columnInfoRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out = append(out, fmt.Sprintf("%s|%s|notnull=%d|default=%s|pk=%d", name, ctype, notNull, dflt.String, pk))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s) rows: %v", table, err)
	}
	sort.Strings(out)
	return out
}

// indexInfoRows returns every index on table as "unique=0/1 cols=[...]",
// name-sorted so index shape (not the arbitrary index name) drives the
// comparison.
func indexInfoRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA index_list(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA index_list(%s): %v", table, err)
	}
	type namedIndex struct {
		name   string
		unique int
	}
	var idxs []namedIndex
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		idxs = append(idxs, namedIndex{name: name, unique: unique})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("index_list(%s) rows: %v", table, err)
	}
	rows.Close()

	var out []string
	for _, ix := range idxs {
		cols, err := db.Query(`PRAGMA index_info(` + ix.name + `)`)
		if err != nil {
			t.Fatalf("PRAGMA index_info(%s): %v", ix.name, err)
		}
		var colNames []string
		for cols.Next() {
			var seqno, cid int
			var cname string
			if err := cols.Scan(&seqno, &cid, &cname); err != nil {
				cols.Close()
				t.Fatalf("scan index_info(%s): %v", ix.name, err)
			}
			colNames = append(colNames, cname)
		}
		if err := cols.Err(); err != nil {
			cols.Close()
			t.Fatalf("index_info(%s) rows: %v", ix.name, err)
		}
		cols.Close()
		out = append(out, fmt.Sprintf("unique=%d cols=%v", ix.unique, colNames))
	}
	sort.Strings(out)
	return out
}

// TestApplyMigrations_Migration2_RejectsDuplicateFillsWithActionableError
// proves the precondition guarding migration 2's UNIQUE(order_intent_id)
// index: a published-v1 database that already has more than one fill row
// for the same intent must fail startup with a clear, actionable error
// (naming the offending intent and stating the remedy) instead of aborting
// on an opaque SQLite constraint violation, and must not partially apply the
// migration or silently repair the duplicates.
func TestApplyMigrations_Migration2_RejectsDuplicateFillsWithActionableError(t *testing.T) {
	ctx := context.Background()
	db := rawTestDB(t)

	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	var v1 migration
	haveV1 := false
	for _, m := range migs {
		if m.version == 1 {
			v1, haveV1 = m, true
			break
		}
	}
	if !haveV1 {
		t.Fatal("migration version 1 not found among embedded migrations")
	}
	if err := applyMigrations(ctx, db, []migration{v1}, fixedNow); err != nil {
		t.Fatalf("apply v1 alone: %v", err)
	}

	now := timeText(fixedNow())
	if _, err := db.ExecContext(ctx, `INSERT INTO instruments (uid, figi, ticker, class_code, lot, min_price_increment, currency, name, cached_at)
		VALUES ('uid-1', 'figi-1', 'TICK', 'TQBR', 10, '0.01', 'RUB', 'Test', ?)`, now); err != nil {
		t.Fatalf("seed instrument: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO cycles (started_at, as_of, mode, engine, engine_version, prompt_template_hash, config_snapshot, status)
		VALUES (?, ?, 'paper', 'rules', 'v1', 'hash', '{}', 'done')`, now, now); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO decisions (cycle_id, instrument_uid, action, qty, order_type, time_in_force, rationale, confidence, validation_status)
		VALUES (1, 'uid-1', 'buy', 1, 'market', 'day', 'because', 1.0, 'ok')`); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO order_intents (client_order_id, decision_id, instrument_uid, side, qty, type, time_in_force, state, created_at, updated_at)
		VALUES ('dup-intent', 1, 'uid-1', 'buy', 1, 'market', 'day', 'filled', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed order intent: %v", err)
	}
	// Two fills for the same intent — legal under the restored v1 schema,
	// which is exactly the gap migration 2's unique index closes.
	if _, err := db.ExecContext(ctx, `INSERT INTO fills (order_intent_id, price, qty, fee, ts) VALUES ('dup-intent', '100', 1, '0.1', ?)`, now); err != nil {
		t.Fatalf("seed first duplicate fill: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fills (order_intent_id, price, qty, fee, ts) VALUES ('dup-intent', '101', 1, '0.1', ?)`, now); err != nil {
		t.Fatalf("seed second duplicate fill: %v", err)
	}

	err = applyMigrations(ctx, db, migs, fixedNow)
	if err == nil {
		t.Fatal("expected applying migration 2 to fail on a database with duplicate fills, got nil error")
	}
	if !strings.Contains(err.Error(), "dup-intent") {
		t.Errorf("error %q does not name the offending order_intent_id (dup-intent)", err.Error())
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "delete") && !strings.Contains(lower, "recreate") {
		t.Errorf("error %q does not state the paper-mode remedy (delete/recreate)", err.Error())
	}

	// Migration 2 must not have partially applied, and the duplicates must
	// not have been silently repaired.
	var v2Count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&v2Count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if v2Count != 0 {
		t.Errorf("schema_migrations has %d rows for version 2, want 0 (a failed precondition must not record the migration as applied)", v2Count)
	}
	var fillCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fills WHERE order_intent_id = 'dup-intent'`).Scan(&fillCount); err != nil {
		t.Fatalf("count fills: %v", err)
	}
	if fillCount != 2 {
		t.Errorf("fills for dup-intent = %d, want 2 (no automatic repair)", fillCount)
	}
}

// TestApplyMigrations_UpgradeAppliesOnlyPending drives applyMigrations with a
// synthetic set: it applies v1 (creating a table with a row), then re-runs with
// [v1, v2] and confirms only v2 is applied while v1's data is preserved.
func TestApplyMigrations_UpgradeAppliesOnlyPending(t *testing.T) {
	ctx := context.Background()
	db := rawTestDB(t)

	m1 := testMigration(1, "one", `CREATE TABLE a (x INTEGER NOT NULL); INSERT INTO a (x) VALUES (7);`)
	if err := applyMigrations(ctx, db, []migration{m1}, fixedNow); err != nil {
		t.Fatalf("apply v1: %v", err)
	}

	m2 := testMigration(2, "two", `CREATE TABLE b (y INTEGER NOT NULL);`)
	if err := applyMigrations(ctx, db, []migration{m1, m2}, fixedNow); err != nil {
		t.Fatalf("upgrade to v2: %v", err)
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if version != 2 {
		t.Errorf("version after upgrade = %d, want 2", version)
	}

	// v2's table exists and v1's row was not re-run or dropped.
	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='b'`).Scan(&name); err != nil {
		t.Errorf("table b missing after upgrade: %v", err)
	}
	var x, count int
	if err := db.QueryRowContext(ctx, `SELECT x FROM a`).Scan(&x); err != nil {
		t.Fatalf("read a: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM a`).Scan(&count); err != nil {
		t.Fatalf("count a: %v", err)
	}
	if x != 7 || count != 1 {
		t.Errorf("table a = {x:%d count:%d}, want {x:7 count:1} (v1 must not re-run)", x, count)
	}
}

// TestApplyMigrations_FailingMigrationRollsBackAtomically proves a migration
// whose SQL fails partway leaves neither its DDL nor its schema_migrations row
// behind: the table its first statement created is gone, and no version row was
// recorded, while the previously applied migration remains.
func TestApplyMigrations_FailingMigrationRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	db := rawTestDB(t)

	m1 := testMigration(1, "one", `CREATE TABLE a (x INTEGER NOT NULL);`)
	// m2's first statement succeeds (creates b); its second fails (unknown
	// table). Both must roll back together with the version row.
	m2bad := testMigration(2, "two_bad", `CREATE TABLE b (y INTEGER NOT NULL);
INSERT INTO does_not_exist (z) VALUES (1);`)

	if err := applyMigrations(ctx, db, []migration{m1, m2bad}, fixedNow); err == nil {
		t.Fatal("expected the failing migration to error, got nil")
	}

	// v1 stayed applied; v2 did not.
	var maxVersion int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&maxVersion); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if maxVersion != 1 {
		t.Errorf("MAX(version) = %d, want 1 (failed migration must not be recorded)", maxVersion)
	}
	var v2rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&v2rows); err != nil {
		t.Fatalf("query v2 rows: %v", err)
	}
	if v2rows != 0 {
		t.Errorf("schema_migrations has %d rows for version 2, want 0 (metadata must roll back)", v2rows)
	}

	// The DDL from the failed migration rolled back with it: table b is gone.
	var name string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='b'`).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("table b lookup err = %v, want sql.ErrNoRows (DDL must roll back)", err)
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
