package execution

import (
	"context"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// Executor is the port the decision cycle drives to place and settle orders. A
// paper simulator and a live broker adapter both satisfy it; the cycle depends
// only on this interface.
//
// Submit journals an intent for every actionable decision and moves it to a
// resting state — it never fills synchronously (DESIGN §7: a decision made on a
// completed candle fills at the next observation, never inside the same bar).
// OnQuote is the observation stream: each fresh quote is a chance for a resting
// order to fill.
type Executor interface {
	// Submit journals and submits an order intent for each actionable decision
	// in ds, using sc for the per-instrument trading parameters, freshest
	// quote, session window and persisted decision ids. It returns an error
	// only for infrastructure failures; a decision that cannot become a valid
	// order (unknown instrument data) is recorded as a rejected intent, not an
	// error return.
	Submit(ctx context.Context, ds []model.Decision, sc SubmitContext) error

	// OnQuote offers q to every order resting on q's instrument, filling those
	// whose conditions are met. It is safe to call concurrently with Submit.
	OnQuote(ctx context.Context, q model.Quote) error
}

// SubmitContext is everything Submit needs beyond the decisions themselves: the
// trading parameters and freshest quote per instrument, the session window that
// gates fills, and the persisted decision ids the journal links intents to.
type SubmitContext struct {
	// Instruments provides the trading parameters (lot, price tick) and the
	// freshest known quote for every instrument a decision may reference. A
	// decision whose instrument is absent here cannot be journaled (no decision
	// linkage, no tick) and is skipped with an event.
	Instruments map[model.InstrumentUID]InstrumentContext

	// DecisionIDs are the decisions table ids for the decisions passed to
	// Submit, aligned by index: DecisionIDs[i] links the intent journaled for
	// ds[i]. Its length must equal len(ds). (This field is not in DESIGN's
	// prose but is forced by order_intents.decision_id being a NOT NULL foreign
	// key; the cycle persists decisions before executing them and passes the
	// ids here.)
	DecisionIDs []int64

	// Session bounds the current trading session. A fill is only attempted when
	// the observation falls inside it; outside the session orders rest.
	Session Session
}

// InstrumentContext pairs an instrument's trading parameters with its freshest
// quote and its authoritative trading permissions, as seen at Submit time.
type InstrumentContext struct {
	Instrument model.Instrument
	Quote      model.Quote

	// TradingStatus is the broker's trading-status token for the instrument
	// (informational). When non-empty, Submit persists the permissions below so
	// OnQuote can gate fills on them across a restart; an empty TradingStatus
	// means "not provided" and leaves the instrument unrestricted.
	TradingStatus string
	// BuyAvailable and SellAvailable are the authoritative per-side permissions
	// a fill is gated on when TradingStatus is set: a suspended or side-disabled
	// instrument does not fill.
	BuyAvailable  bool
	SellAvailable bool
}

// Session is the current trading day's window in absolute time. The zero value
// (both bounds zero) means 24-hour trading with no session restriction, matching
// a config with no session hours set.
type Session struct {
	Start time.Time
	End   time.Time
}

// IsOpen reports whether at falls within the session. A zero-value Session is
// always open. The window is half-open [Start, End): a quote exactly at End is
// out of session, so the close instant belongs to expiry, not to a fill.
func (s Session) IsOpen(at time.Time) bool {
	if s.Start.IsZero() && s.End.IsZero() {
		return true
	}
	return !at.Before(s.Start) && at.Before(s.End)
}

// FillApplier is execution's consumer-owned view of internal/portfolio: given a
// fill inside an open transaction, it applies the position, cash and PnL
// effects. Execution inserts the fills row itself (DESIGN §3) and calls
// ApplyFill with the same Querier so the whole settlement commits atomically.
type FillApplier interface {
	ApplyFill(ctx context.Context, q sqlite.Querier, fa FillApplication) error
}

// FillApplication is the payload handed to FillApplier.ApplyFill. It carries the
// fill plus the facts portfolio needs to value it: which instrument and side,
// the lot size that converts lots to shares, the instrument's settlement
// currency, and whether the price came from the last-price fallback
// (LowFidelity) rather than a real bid/ask.
type FillApplication struct {
	Fill          model.Fill
	InstrumentUID model.InstrumentUID
	Side          model.Side
	Lot           int64
	// Currency is the instrument's settlement currency, carried so the
	// portfolio can reject a fill that would post into a different currency
	// than the account settles in rather than silently booking, say, a USD
	// notional as RUB.
	Currency    string
	LowFidelity bool
}
