package execution

import (
	"context"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// Journal is the order-intent journal: the durable, append-then-CAS record of
// every order the robot means to place (DESIGN §4). It is the only sanctioned
// way execution mutates an intent's state — callers never UPDATE the state
// column directly, so a terminal intent can never silently regress and two
// writers racing on one intent cannot both win (the store's compare-and-swap
// enforces both).
type Journal struct {
	clock clock.Clock
}

// NewJournal returns a Journal that stamps intents from clk.
func NewJournal(clk clock.Clock) *Journal { return &Journal{clock: clk} }

// NewIntent is the request to open a journal entry: everything about an order
// except its identity (a fresh client order id is minted by Open) and its state
// (always `new` at open).
type NewIntent struct {
	DecisionID    int64
	InstrumentUID model.InstrumentUID
	Side          model.Side
	Qty           int64 // lots; must be > 0
	Type          model.OrderType
	LimitPrice    *model.Decimal
	TimeInForce   model.TimeInForce
}

// Open mints a fresh UUID client order id, persists the intent in state `new`,
// and returns the stored record. This is the "journal before anything" step:
// the durable row exists before any submission or fill so a crash mid-flight
// always leaves a reconcilable trace. q may be the database or a transaction.
func (j *Journal) Open(ctx context.Context, q sqlite.Querier, ni NewIntent) (model.OrderIntent, error) {
	id, err := NewClientOrderID()
	if err != nil {
		return model.OrderIntent{}, err
	}
	now := j.clock.Now()
	in := model.OrderIntent{
		ClientOrderID: id,
		DecisionID:    ni.DecisionID,
		InstrumentUID: ni.InstrumentUID,
		Side:          ni.Side,
		Qty:           ni.Qty,
		Type:          ni.Type,
		LimitPrice:    ni.LimitPrice,
		TimeInForce:   ni.TimeInForce,
		State:         model.IntentNew,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := (sqlite.IntentRepo{}).Insert(ctx, q, in); err != nil {
		return model.OrderIntent{}, err
	}
	return in, nil
}

// Transition advances the intent identified by id from one state to the next
// through the store's compare-and-swap. It surfaces the store's typed errors
// unchanged: IllegalTransitionError for a non-edge (including any move out of a
// terminal state), StateConflictError when the stored state is not from, and
// ErrNotFound for a missing intent. The updated_at stamp comes from the clock.
func (j *Journal) Transition(ctx context.Context, q sqlite.Querier, id string, from, to model.IntentState) error {
	return (sqlite.IntentRepo{}).UpdateState(ctx, q, id, from, to, j.clock.Now())
}

// TransitionWithReason is Transition plus recording a human-readable reason
// on the intent row (order_intents.reason) in the same statement — for
// rejected/canceled transitions where the prose belongs on the row, not only
// in the events log. This still goes through the store's compare-and-swap
// (Journal remains the only sanctioned way execution mutates an intent's
// state); it does not bypass it.
func (j *Journal) TransitionWithReason(ctx context.Context, q sqlite.Querier, id string, from, to model.IntentState, reason string) error {
	return (sqlite.IntentRepo{}).UpdateStateWithReason(ctx, q, id, from, to, j.clock.Now(), reason)
}
