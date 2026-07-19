package decision

import (
	"encoding/json"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Response is the strict output every decision engine returns for one
// cycle: a batch of per-instrument actions plus optional free-text notes
// (e.g. which instruments were skipped and why).
//
// Actions reuses model.Decision directly (per DESIGN.md §6/§8: the same value
// flows from engine output through validation and risk adjustment into the
// decisions table), but model.Decision is shared, engine-agnostic domain state
// with no JSON tags of its own. The LLM contract requires stable snake_case
// field names, so Response marshals and unmarshals each action through
// wireAction — the wire shape is pinned here (round-trip test in
// response_test.go) without leaking JSON concerns into internal/model.
type Response struct {
	Actions []model.Decision
	Notes   string
}

// wireAction is the snake_case JSON shape of one action on the decision wire
// contract. It mirrors model.Decision field-for-field; the tags are the
// contract the LLM engines produce and consume.
type wireAction struct {
	InstrumentUID model.InstrumentUID `json:"instrument_uid"`
	Action        model.Action        `json:"action"`
	Quantity      int64               `json:"quantity"`
	OrderType     model.OrderType     `json:"order_type"`
	LimitPrice    *model.Decimal      `json:"limit_price,omitempty"`
	TimeInForce   model.TimeInForce   `json:"time_in_force"`
	Rationale     string              `json:"rationale,omitempty"`
	Confidence    float64             `json:"confidence"`
}

func toWireAction(d model.Decision) wireAction {
	return wireAction{
		InstrumentUID: d.InstrumentUID,
		Action:        d.Action,
		Quantity:      d.Quantity,
		OrderType:     d.OrderType,
		LimitPrice:    d.LimitPrice,
		TimeInForce:   d.TimeInForce,
		Rationale:     d.Rationale,
		Confidence:    d.Confidence,
	}
}

func (w wireAction) toDecision() model.Decision {
	return model.Decision{
		InstrumentUID: w.InstrumentUID,
		Action:        w.Action,
		Quantity:      w.Quantity,
		OrderType:     w.OrderType,
		LimitPrice:    w.LimitPrice,
		TimeInForce:   w.TimeInForce,
		Rationale:     w.Rationale,
		Confidence:    w.Confidence,
	}
}

// responseWire is the JSON envelope: a non-nil (possibly empty) actions array
// so the key is always present, plus optional notes.
type responseWire struct {
	Actions []wireAction `json:"actions"`
	Notes   string       `json:"notes,omitempty"`
}

// MarshalJSON encodes the Response with snake_case action fields (the LLM wire
// contract). The actions array is always present, even when empty.
func (r Response) MarshalJSON() ([]byte, error) {
	out := responseWire{Actions: make([]wireAction, len(r.Actions)), Notes: r.Notes}
	for i, d := range r.Actions {
		out.Actions[i] = toWireAction(d)
	}
	return json.Marshal(out)
}

// UnmarshalJSON decodes a snake_case wire response into model.Decision actions.
func (r *Response) UnmarshalJSON(b []byte) error {
	var in responseWire
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	r.Notes = in.Notes
	r.Actions = make([]model.Decision, len(in.Actions))
	for i, w := range in.Actions {
		r.Actions[i] = w.toDecision()
	}
	return nil
}
