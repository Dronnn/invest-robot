package decision

import (
	"time"

	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/model"
)

// Request is the full, self-contained context a decision engine reasons
// over for one cycle. Every field it needs travels here — nothing an engine
// decides is allowed to depend on I/O or wall-clock state it reads itself.
// AsOf is the cycle's as-of watermark (UTC): every piece of data in Request
// was assembled from data with event_time <= AsOf (DESIGN.md §6's
// completed-candle discipline).
type Request struct {
	AsOf  time.Time `json:"as_of"`
	Mode  string    `json:"mode"`
	Cycle CycleMeta `json:"cycle"`

	Portfolio   Portfolio           `json:"portfolio"`
	OpenIntents []IntentView        `json:"open_intents"`
	Instruments []InstrumentContext `json:"instruments"`

	// Limits mirrors the configured risk limits (config.RiskConfig) for the
	// engine's own information — e.g. to size within them — but is purely
	// informational: risk enforcement itself lives in internal/risk and
	// always has the last word (DESIGN.md §8).
	Limits Limits `json:"limits"`

	// RecentOutcomes is the last N decisions with a summary of what
	// happened to them, giving the engine short-term memory across cycles.
	// Empty until the store-backed populator lands in a later step.
	RecentOutcomes []OutcomeView `json:"recent_outcomes"`
}

// CycleMeta identifies the cycle a Request belongs to.
type CycleMeta struct {
	ID        int64     `json:"id"`
	StartedAt time.Time `json:"started_at"`
	// Interval is the configured decision cadence (e.g. "5m"), given as
	// context for interpreting the attached feature snapshots.
	Interval string `json:"interval"`
}

// Portfolio is the account state visible to the engine: cash, mark-to-market
// equity, and open positions.
type Portfolio struct {
	Cash      model.Decimal  `json:"cash"`
	Equity    model.Decimal  `json:"equity"`
	Positions []PositionView `json:"positions"`
}

// PositionView is one held position, valued at the last known price.
type PositionView struct {
	UID           model.InstrumentUID `json:"uid"`
	Ticker        string              `json:"ticker"`
	Qty           int64               `json:"qty"`
	AvgPrice      model.Decimal       `json:"avg_price"`
	LastPrice     model.Decimal       `json:"last_price"`
	UnrealizedPnL model.Decimal       `json:"unrealized_pnl"`
}

// IntentView is an open (non-terminal) order intent, so the engine knows
// what is already working before proposing new actions.
type IntentView struct {
	ClientOrderID string              `json:"client_order_id"`
	InstrumentUID model.InstrumentUID `json:"instrument_uid"`
	Side          model.Side          `json:"side"`
	Qty           int64               `json:"qty"`
	Type          model.OrderType     `json:"type"`
	LimitPrice    *model.Decimal      `json:"limit_price,omitempty"`
	TimeInForce   model.TimeInForce   `json:"time_in_force"`
	State         model.IntentState   `json:"state"`
	CreatedAt     time.Time           `json:"created_at"`
}

// QuoteView is a point-in-time top-of-book snapshot, respecting the same
// as-of discipline as the rest of Request.
type QuoteView struct {
	Bid  model.Decimal `json:"bid"`
	Ask  model.Decimal `json:"ask"`
	Last model.Decimal `json:"last"`
	TS   time.Time     `json:"ts"`
}

// InstrumentContext is everything the engine needs to reason about one
// instrument: its identity and trading parameters, the latest quote, its
// computed feature snapshot, and how stale that data is.
type InstrumentContext struct {
	UID       model.InstrumentUID `json:"uid"`
	FIGI      model.FIGI          `json:"figi"`
	Ticker    string              `json:"ticker"`
	ClassCode string              `json:"class_code"`

	Lot               int64         `json:"lot"`
	MinPriceIncrement model.Decimal `json:"min_price_increment"`

	Quote    QuoteView         `json:"quote"`
	Features features.Snapshot `json:"features"`

	// PrevEMATrend is the EMA trend classification from the previous cycle's
	// feature snapshot for this instrument, carried from the snapshot lineage
	// so a strategy can detect an actual EMA crossover (a transition into
	// bullish/bearish) rather than re-firing every cycle a level holds. Empty
	// when no prior snapshot exists yet (e.g. the first cycle after startup),
	// in which case a crossover-based strategy degrades to the current level.
	PrevEMATrend features.EMATrend `json:"prev_ema_trend,omitempty"`

	// DataFreshness is how old the newest data behind Quote/Features is,
	// relative to Request.AsOf. Tagged _ns because time.Duration's default
	// JSON encoding is already its int64 nanosecond count.
	DataFreshness time.Duration `json:"data_freshness_ns"`
}

// Limits mirrors config.RiskConfig, expressed in model.Decimal instead of
// config's raw strings. Informational for the engine; internal/risk is the
// actual enforcement point (DESIGN.md §6, §8).
type Limits struct {
	MaxPositionNotional model.Decimal `json:"max_position_notional"`
	MaxTotalExposure    model.Decimal `json:"max_total_exposure"`
	MaxOrdersPerCycle   int           `json:"max_orders_per_cycle"`
	MaxOrdersPerDay     int           `json:"max_orders_per_day"`
	MaxDailyLoss        model.Decimal `json:"max_daily_loss"`
	Allowlist           []string      `json:"allowlist,omitempty"`
	CashFloor           model.Decimal `json:"cash_floor"`
}

// OutcomeView summarizes what happened to a past decision, so the engine has
// short-term memory across cycles. Population from the store lands in a
// later step; this defines the shape.
type OutcomeView struct {
	CycleID       int64               `json:"cycle_id"`
	AsOf          time.Time           `json:"as_of"`
	InstrumentUID model.InstrumentUID `json:"instrument_uid"`
	Action        model.Action        `json:"action"`
	Qty           int64               `json:"qty"`
	FillPrice     *model.Decimal      `json:"fill_price,omitempty"`
	RealizedPnL   *model.Decimal      `json:"realized_pnl,omitempty"`
	// Outcome is a short free-text summary, e.g. "filled", "rejected:
	// risk_max_notional", "hold".
	Outcome string `json:"outcome"`
}
