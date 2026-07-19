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

// TestResponse_WireFormatIsSnakeCase pins the exact JSON keys of the action
// wire contract: the LLM engines depend on stable snake_case field names, so a
// silent rename to Go field names must fail this test.
func TestResponse_WireFormatIsSnakeCase(t *testing.T) {
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
		},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var envelope struct {
		Actions []map[string]json.RawMessage `json:"actions"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if len(envelope.Actions) != 1 {
		t.Fatalf("actions len = %d, want 1 (%s)", len(envelope.Actions), raw)
	}
	action := envelope.Actions[0]
	wantKeys := []string{"instrument_uid", "action", "quantity", "order_type", "limit_price", "time_in_force", "rationale", "confidence"}
	for _, k := range wantKeys {
		if _, ok := action[k]; !ok {
			t.Errorf("action missing snake_case key %q: %s", k, raw)
		}
	}
	// No Go-cased field names must leak onto the wire.
	for _, k := range []string{"InstrumentUID", "Action", "OrderType", "LimitPrice", "TimeInForce"} {
		if _, ok := action[k]; ok {
			t.Errorf("action carries Go-cased key %q; the wire contract is snake_case: %s", k, raw)
		}
	}
}

// TestResponse_MarketOrderOmitsLimitPrice confirms a market order carries no
// limit_price key on the wire, and the omission round-trips back to a nil
// LimitPrice.
func TestResponse_MarketOrderOmitsLimitPrice(t *testing.T) {
	resp := Response{Actions: []model.Decision{{
		InstrumentUID: "GAZP-UID", Action: model.ActionHold, OrderType: model.OrderMarket, TimeInForce: model.TIFDay,
	}}}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var envelope struct {
		Actions []map[string]json.RawMessage `json:"actions"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := envelope.Actions[0]["limit_price"]; ok {
		t.Errorf("market order should omit limit_price: %s", raw)
	}
	var back Response
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if back.Actions[0].LimitPrice != nil {
		t.Errorf("round-tripped LimitPrice = %v, want nil", back.Actions[0].LimitPrice)
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
