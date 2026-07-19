package risk

import (
	"github.com/Dronnn/invest-robot/internal/model"
)

// Position is the risk-relevant view of an existing holding in one
// instrument. LastPrice marks the position for the notional/exposure checks
// (rules 5-6); AvgPrice is carried for completeness but is never used by
// this package — AvgPrice is cost basis, not current risk, and using it
// would let a paper gain quietly widen how much more the position is
// allowed to grow.
type Position struct {
	QtyLots   int64
	LastPrice model.Decimal
	AvgPrice  model.Decimal
}

// PendingIntents is the risk-relevant view of resting order-intent lots for
// one instrument, carried over from earlier cycles that haven't reached a
// terminal state yet.
//
// BuyLots values exposure (rules 5-6) and reserves cash (rule 7): a pending
// buy commits capital that hasn't landed yet, so counting it prevents the same
// capital being committed twice across cycles before the first order fills. It
// deliberately never nets *down* an exposure or cash limit — a pending buy
// only ever adds commitment, never headroom.
//
// SellLots backs the oversell rule: lots already resting to sell are
// subtracted from the position when sizing new sells, so total pending sells
// can never exceed what is held (Phase 1 forbids shorting). It too only ever
// reduces sellable quantity; it never offsets a buy-side limit, since a
// pending sell is not a confirmed reduction of exposure until it fills and
// letting it widen a buy budget would lean on an order that might not fill,
// which DESIGN.md §8 rules out.
type PendingIntents struct {
	BuyLots  int64
	SellLots int64
}

// State is everything Check needs to evaluate one cycle's proposed actions.
// It is a snapshot the caller assembles from the portfolio and the latest
// market data; risk holds no state of its own between calls.
type State struct {
	// Cash is the account's free cash balance before this cycle's actions.
	Cash model.Decimal

	// DayPnL is today's cumulative profit/loss — realized fills plus
	// unrealized mark-to-market change since the trading day began —
	// positive for a gain, negative for a loss. A single signed figure was
	// chosen over separate realized/unrealized fields because the only rule
	// that consumes it (the kill switch) only ever needs the sum; the
	// caller decides how it tracks realized vs. unrealized internally.
	DayPnL model.Decimal

	// OrdersToday is the count of order-producing decisions (buy/sell/
	// close) already placed earlier today, before this cycle.
	OrdersToday int

	// Positions is the current holding per instrument. An instrument with
	// no entry is treated as flat (zero quantity).
	Positions map[model.InstrumentUID]Position

	// OpenIntents is resting (not yet filled/canceled/rejected) order
	// intent lots per instrument, carried over from earlier cycles. An
	// instrument with no entry has none pending.
	OpenIntents map[model.InstrumentUID]PendingIntents

	// Quotes is the latest known top-of-book snapshot per instrument, used
	// to price candidate buys: ask when available, else last (see
	// priceForBuy). An instrument with no entry cannot be priced from a
	// quote — Positions[uid].LastPrice is the fallback for valuing existing
	// exposure, but a candidate buy with no quote and no limit price cannot
	// be evaluated and is stripped.
	Quotes map[model.InstrumentUID]model.Quote

	// Instruments carries lot size for every instrument any decision or
	// position might reference. A decision for a UID missing here cannot be
	// sized in shares and is stripped by the first rule that needs to price
	// it.
	Instruments map[model.InstrumentUID]model.Instrument

	// SlippageBufferBps pads a market buy's cost estimate, in basis points,
	// for the cash-floor check only (rule 7); it does not affect the
	// notional/exposure valuation of rules 5-6, which price the resulting
	// position, not an execution. The caller resolves this from
	// config.Paper.SlippageBps, or leaves it 0 to disable the buffer.
	SlippageBufferBps int64

	// FeeBufferBps reserves a basis-point fraction of a buy's notional as
	// an estimated commission for the cash-floor check only (rule 7). The
	// caller resolves this from config.Paper.CommissionRate, or leaves it 0
	// to disable the buffer.
	FeeBufferBps int64
}

// Rule identifies which configured limit produced an Adjustment.
type Rule string

const (
	RuleDailyLossKillSwitch Rule = "daily_loss_kill_switch"
	RuleAllowlist           Rule = "allowlist"
	RuleMaxOrdersPerCycle   Rule = "max_orders_per_cycle"
	RuleMaxOrdersPerDay     Rule = "max_orders_per_day"
	RuleMaxPositionNotional Rule = "max_position_notional"
	RuleMaxTotalExposure    Rule = "max_total_exposure"
	RuleCashFloor           Rule = "cash_floor"
	RuleOversell            Rule = "oversell"
)

// Adjustment is an audit record of one modification Check made to a
// decision. Original is the decision's value immediately before this rule
// touched it (which may already be a value an earlier rule shrank) and
// Adjusted is its value immediately after: chaining every Adjustment for a
// given Index in order reconstructs the full history. Adjusted is nil when
// the rule stripped the decision entirely (it is removed from Result.
// Allowed); otherwise it holds the shrunk decision.
type Adjustment struct {
	Index         int
	InstrumentUID model.InstrumentUID
	Rule          Rule
	Original      model.Decision
	Adjusted      *model.Decision
	Reason        string
}

// Result is the outcome of a risk check: the actions that survived, in
// stable input order; every adjustment made along the way, in the order the
// rules ran; and whether the daily-loss kill switch is currently engaged.
type Result struct {
	Allowed     []model.Decision
	Adjustments []Adjustment
	Halted      bool
}
