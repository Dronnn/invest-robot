package model

import (
	"fmt"
	"time"
)

// Side is the direction of an order.
type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

func (s Side) String() string { return string(s) }

// Valid reports whether s is a known side.
func (s Side) Valid() bool { return s == SideBuy || s == SideSell }

// ParseSide parses a side token.
func ParseSide(s string) (Side, error) {
	v := Side(s)
	if !v.Valid() {
		return "", fmt.Errorf("model: invalid side %q", s)
	}
	return v, nil
}

// OrderType is how an order is priced.
type OrderType string

const (
	OrderMarket OrderType = "market"
	OrderLimit  OrderType = "limit"
)

func (t OrderType) String() string { return string(t) }

// Valid reports whether t is a known order type.
func (t OrderType) Valid() bool { return t == OrderMarket || t == OrderLimit }

// ParseOrderType parses an order-type token.
func ParseOrderType(s string) (OrderType, error) {
	v := OrderType(s)
	if !v.Valid() {
		return "", fmt.Errorf("model: invalid order type %q", s)
	}
	return v, nil
}

// TimeInForce controls how long an unfilled order rests. The set is kept
// minimal for Phase 1.
type TimeInForce string

const (
	TIFDay TimeInForce = "day" // rest until end of session
	TIFIOC TimeInForce = "ioc" // immediate-or-cancel
)

func (t TimeInForce) String() string { return string(t) }

// Valid reports whether t is a known time-in-force.
func (t TimeInForce) Valid() bool { return t == TIFDay || t == TIFIOC }

// ParseTimeInForce parses a time-in-force token.
func ParseTimeInForce(s string) (TimeInForce, error) {
	v := TimeInForce(s)
	if !v.Valid() {
		return "", fmt.Errorf("model: invalid time in force %q", s)
	}
	return v, nil
}

// IntentState is the lifecycle state of an order intent. The machine is:
//
//	new ── submitted ── acked ── filled | canceled | rejected
//	                 └───────────────────── unknown
//
// filled, canceled, and rejected are terminal. unknown is the "outcome not
// known" state entered when a broker call returns an ambiguous result (tinvest
// exit 7); it is non-terminal and resolves to a real state after
// reconciliation.
type IntentState string

const (
	IntentNew       IntentState = "new"
	IntentSubmitted IntentState = "submitted"
	IntentAcked     IntentState = "acked"
	IntentFilled    IntentState = "filled"
	IntentCanceled  IntentState = "canceled"
	IntentRejected  IntentState = "rejected"
	IntentUnknown   IntentState = "unknown"
)

func (s IntentState) String() string { return string(s) }

// Valid reports whether s is a known intent state.
func (s IntentState) Valid() bool {
	switch s {
	case IntentNew, IntentSubmitted, IntentAcked, IntentFilled,
		IntentCanceled, IntentRejected, IntentUnknown:
		return true
	default:
		return false
	}
}

// ParseIntentState parses an intent-state token.
func ParseIntentState(s string) (IntentState, error) {
	v := IntentState(s)
	if !v.Valid() {
		return "", fmt.Errorf("model: invalid intent state %q", s)
	}
	return v, nil
}

// IsTerminal reports whether s admits no further transitions.
func (s IntentState) IsTerminal() bool {
	switch s {
	case IntentFilled, IntentCanceled, IntentRejected:
		return true
	default:
		return false
	}
}

// intentTransitions is the allowed-edge set of the intent state machine.
var intentTransitions = map[IntentState]map[IntentState]bool{
	IntentNew: {
		IntentSubmitted: true,
		IntentRejected:  true, // rejected locally before submission
		IntentUnknown:   true, // spawn outcome unknown before we saw an ack
	},
	IntentSubmitted: {
		IntentAcked:    true,
		IntentFilled:   true,
		IntentCanceled: true,
		IntentRejected: true,
		IntentUnknown:  true,
	},
	IntentAcked: {
		IntentFilled:   true,
		IntentCanceled: true,
		IntentRejected: true,
		IntentUnknown:  true,
	},
	IntentUnknown: { // resolved by reconciliation
		IntentSubmitted: true,
		IntentAcked:     true,
		IntentFilled:    true,
		IntentCanceled:  true,
		IntentRejected:  true,
	},
	IntentFilled:   {},
	IntentCanceled: {},
	IntentRejected: {},
}

// CanTransition reports whether the intent state machine permits moving from
// one state to another. A same-state pair is not a transition and returns
// false.
func CanTransition(from, to IntentState) bool {
	return intentTransitions[from][to]
}

// OrderIntent is the robot's durable record of an order it means to place,
// keyed by a stable client order ID and written before any broker call. Qty is
// in lots. LimitPrice is nil for market orders.
type OrderIntent struct {
	ClientOrderID string
	DecisionID    int64
	InstrumentUID InstrumentUID
	Side          Side
	Qty           int64
	Type          OrderType
	LimitPrice    *Decimal
	TimeInForce   TimeInForce
	State         IntentState
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Fill is an execution against an intent. IntentID is the owning intent's
// ClientOrderID. Qty is in lots. Fee is the commission charged for this fill.
type Fill struct {
	IntentID string
	Price    Decimal
	Qty      int64
	Fee      Decimal
	TS       time.Time
}
