package portfolio

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// openTest opens a throwaway migrated SQLite database for one test, mirroring
// internal/store/sqlite's own openTest helper (unexported there, so this
// package needs its own copy against the public API).
func openTest(t *testing.T) *sqlite.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "robot.db")
	db, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// nowUTC returns a UTC time truncated to microsecond precision, matching what
// round-trips losslessly through the store's RFC3339-ish TEXT columns.
func nowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

const testCurrency = "rub"

// seedInstrument upserts a minimal instrument with the given uid and lot
// size so position/quote tests have InstrumentRepo metadata to resolve.
func seedInstrument(t *testing.T, db *sqlite.DB, uid model.InstrumentUID, lot int64) model.Instrument {
	t.Helper()
	i := model.Instrument{
		InstrumentRef: model.InstrumentRef{
			UID:       uid,
			FIGI:      model.FIGI("FIGI-" + uid),
			Ticker:    "TICK",
			ClassCode: "TQBR",
		},
		Lot:               lot,
		MinPriceIncrement: model.MustDecimal("0.01"),
		Currency:          testCurrency,
		Name:              "Test " + string(uid),
	}
	if err := (sqlite.InstrumentRepo{}).Upsert(context.Background(), db, i, nowUTC()); err != nil {
		t.Fatalf("seed instrument %s: %v", uid, err)
	}
	return i
}

// seedIntent chains cycle -> decision -> order_intents rows so a fill's
// order_intent_id foreign key resolves. ApplyFill itself only writes to
// fills/positions/cash_ledger; the intent it accounts for is assumed to
// already exist (written by internal/execution before the broker call, per
// DESIGN.md §4), so tests recreate that precondition here.
func seedIntent(t *testing.T, db *sqlite.DB, uid model.InstrumentUID, clientOrderID string) {
	t.Helper()
	ctx := context.Background()
	now := nowUTC()

	cycleID, err := (sqlite.CycleRepo{}).Insert(ctx, db, sqlite.Cycle{
		StartedAt: now, AsOf: now, Mode: "paper", Engine: "rules",
		EngineVersion: "test", PromptTemplateHash: "test", ConfigSnapshot: "{}", Status: "running",
	})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	decisionID, err := (sqlite.DecisionRepo{}).Insert(ctx, db, sqlite.DecisionRecord{
		CycleID: cycleID,
		Decision: model.Decision{
			InstrumentUID: uid, Action: model.ActionBuy, Quantity: 1,
			OrderType: model.OrderMarket, TimeInForce: model.TIFDay, Rationale: "test", Confidence: 1,
		},
		ValidationStatus: "valid",
	})
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}

	if err := (sqlite.IntentRepo{}).Insert(ctx, db, model.OrderIntent{
		ClientOrderID: clientOrderID, DecisionID: decisionID, InstrumentUID: uid,
		Side: model.SideBuy, Qty: 1, Type: model.OrderMarket, TimeInForce: model.TIFDay,
		State: model.IntentNew, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed intent %s: %v", clientOrderID, err)
	}
}
