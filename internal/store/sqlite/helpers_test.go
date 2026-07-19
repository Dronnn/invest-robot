package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// nowUTC returns a UTC time truncated to microsecond precision, matching
// what round-trips losslessly through RFC3339Nano TEXT storage without
// depending on the platform's monotonic-clock reading being comparable
// post-parse.
func nowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func testInstrument(uid string) model.Instrument {
	return model.Instrument{
		InstrumentRef: model.InstrumentRef{
			UID:       model.InstrumentUID(uid),
			FIGI:      model.FIGI("FIGI-" + uid),
			Ticker:    "TICK",
			ClassCode: "TQBR",
		},
		Lot:               10,
		MinPriceIncrement: model.MustDecimal("0.01"),
		Currency:          "rub",
		Name:              "Test Instrument " + uid,
	}
}

func seedInstrument(t *testing.T, db *DB, uid string) model.Instrument {
	t.Helper()
	i := testInstrument(uid)
	if err := (InstrumentRepo{}).Upsert(context.Background(), db, i, nowUTC()); err != nil {
		t.Fatalf("seed instrument %s: %v", uid, err)
	}
	return i
}

func seedCycle(t *testing.T, db *DB) int64 {
	t.Helper()
	now := nowUTC()
	id, err := (CycleRepo{}).Insert(context.Background(), db, Cycle{
		StartedAt:          now,
		AsOf:               now,
		Mode:               "paper",
		Engine:             "rules",
		EngineVersion:      "v1",
		PromptTemplateHash: "hash",
		ConfigSnapshot:     "{}",
		Status:             "running",
	})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	return id
}

func seedDecision(t *testing.T, db *DB, cycleID int64, uid model.InstrumentUID) int64 {
	t.Helper()
	rec := DecisionRecord{
		CycleID: cycleID,
		Decision: model.Decision{
			InstrumentUID: uid,
			Action:        model.ActionBuy,
			Quantity:      1,
			OrderType:     model.OrderMarket,
			TimeInForce:   model.TIFDay,
			Rationale:     "test rationale",
			Confidence:    0.5,
		},
		ValidationStatus: "valid",
	}
	id, err := (DecisionRepo{}).Insert(context.Background(), db, rec)
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	return id
}

func seedIntent(t *testing.T, db *DB, decisionID int64, uid model.InstrumentUID, clientOrderID string) model.OrderIntent {
	t.Helper()
	now := nowUTC()
	in := model.OrderIntent{
		ClientOrderID: clientOrderID,
		DecisionID:    decisionID,
		InstrumentUID: uid,
		Side:          model.SideBuy,
		Qty:           1,
		Type:          model.OrderMarket,
		TimeInForce:   model.TIFDay,
		State:         model.IntentNew,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := (IntentRepo{}).Insert(context.Background(), db, in); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return in
}
