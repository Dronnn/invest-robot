package decision

import (
	"encoding/json"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
)

func TestResponse_JSONRoundTrip(t *testing.T) {
	limit := model.MustDecimal("100.50")
	resp := Response{
		Actions: []model.Decision{
			{
				InstrumentUID: "SBER-UID",
				Action:        model.ActionBuy,
				Quantity:      3,
				OrderType:     model.OrderLimit,
				LimitPrice:    &limit,
				TimeInForce:   model.TIFDay,
				Rationale:     "trend entry",
				Confidence:    0.72,
			},
			{
				InstrumentUID: "GAZP-UID",
				Action:        model.ActionHold,
				OrderType:     model.OrderMarket,
				TimeInForce:   model.TIFDay,
				Rationale:     "no signal",
				Confidence:    0.5,
			},
		},
		Notes: "ROSN-UID skipped: stale data",
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Response
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	raw2, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal (2): %v", err)
	}
	if string(raw) != string(raw2) {
		t.Fatalf("round trip is not byte-identical:\n%s\nvs\n%s", raw, raw2)
	}

	if len(got.Actions) != 2 {
		t.Fatalf("Actions len = %d, want 2", len(got.Actions))
	}
	if got.Actions[0].LimitPrice == nil || got.Actions[0].LimitPrice.Cmp(limit) != 0 {
		t.Errorf("Actions[0].LimitPrice = %v, want %v", got.Actions[0].LimitPrice, limit)
	}
	if got.Notes != resp.Notes {
		t.Errorf("Notes = %q, want %q", got.Notes, resp.Notes)
	}
}

func TestResponse_NotesOmittedWhenEmpty(t *testing.T) {
	resp := Response{Actions: []model.Decision{}}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := top["notes"]; ok {
		t.Errorf("notes key present for empty Notes: %s", raw)
	}
	if _, ok := top["actions"]; !ok {
		t.Errorf("actions key missing: %s", raw)
	}
}
