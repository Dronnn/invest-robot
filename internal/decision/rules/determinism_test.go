package rules

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Dronnn/invest-robot/internal/decision"
)

// TestDecide_Determinism runs the same Request through the engine twice and
// requires byte-identical JSON output — the property that makes strategy
// tuning and backtest replay trustworthy.
func TestDecide_Determinism(t *testing.T) {
	req := multiInstrumentRequest()

	eng, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp1, _, err := eng.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide (1): %v", err)
	}
	resp2, _, err := eng.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide (2): %v", err)
	}

	raw1, err := json.Marshal(resp1)
	if err != nil {
		t.Fatalf("marshal (1): %v", err)
	}
	raw2, err := json.Marshal(resp2)
	if err != nil {
		t.Fatalf("marshal (2): %v", err)
	}
	if string(raw1) != string(raw2) {
		t.Fatalf("Decide is not deterministic:\n%s\nvs\n%s", raw1, raw2)
	}
}

// TestDecide_DeterminismFreshEngineInstances confirms determinism holds
// across independently constructed engines with identical params, not just
// across repeated calls on the same instance.
func TestDecide_DeterminismFreshEngineInstances(t *testing.T) {
	req := multiInstrumentRequest()

	eng1, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New (1): %v", err)
	}
	eng2, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New (2): %v", err)
	}

	resp1, _, err := eng1.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide (1): %v", err)
	}
	resp2, _, err := eng2.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide (2): %v", err)
	}

	raw1, _ := json.Marshal(resp1)
	raw2, _ := json.Marshal(resp2)
	if string(raw1) != string(raw2) {
		t.Fatalf("independently constructed engines diverged:\n%s\nvs\n%s", raw1, raw2)
	}
}

func multiInstrumentRequest() decision.Request {
	req := baseRequest()
	req.Portfolio.Positions = []decision.PositionView{{UID: "GAZP-UID", Qty: 5}}
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"),
		instrumentFixture("GAZP-UID", 240, 245, 55, 3.0, "140"),
		instrumentFixture("LKOH-UID", 100, 105, 40, 5.0, "6000"),
	}
	return req
}
