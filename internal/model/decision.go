package model

import "fmt"

// Action is what a decision proposes to do with an instrument.
type Action string

const (
	ActionBuy   Action = "buy"
	ActionSell  Action = "sell"
	ActionHold  Action = "hold"  // take no position change
	ActionClose Action = "close" // flatten the existing position
)

func (a Action) String() string { return string(a) }

// Valid reports whether a is a known action.
func (a Action) Valid() bool {
	switch a {
	case ActionBuy, ActionSell, ActionHold, ActionClose:
		return true
	default:
		return false
	}
}

// ParseAction parses an action token.
func ParseAction(s string) (Action, error) {
	v := Action(s)
	if !v.Valid() {
		return "", fmt.Errorf("model: invalid action %q", s)
	}
	return v, nil
}

// Decision is the core, engine-agnostic unit a decision engine emits for one
// instrument. It is shared by every engine (rules, claude-cli, anthropic-api);
// the full request/response JSON contract that wraps a batch of these is built
// in a later step. Quantity is in lots. LimitPrice is nil unless OrderType is
// limit. Confidence is in [0,1].
type Decision struct {
	InstrumentUID InstrumentUID
	Action        Action
	Quantity      int64
	OrderType     OrderType
	LimitPrice    *Decimal
	TimeInForce   TimeInForce
	Rationale     string
	Confidence    float64
}
