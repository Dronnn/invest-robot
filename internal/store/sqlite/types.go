package sqlite

import (
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// The row types below cover tables whose shape is storage/bookkeeping detail
// rather than a pure domain type — internal/model stays free of SQL-only
// concerns (surrogate ids, validation_status, raw engine payloads). They live
// here for Phase 1; if internal/cycle or internal/decision later want to own
// pieces of this shape, promote them then.

// Cycle is a persisted decision-cycle record (DESIGN §5, §6).
type Cycle struct {
	ID                 int64
	StartedAt          time.Time
	AsOf               time.Time
	Mode               string
	Engine             string
	EngineVersion      string
	PromptTemplateHash string
	ConfigSnapshot     string // JSON
	Status             string
}

// DecisionRecord is a persisted decision: the engine-agnostic model.Decision
// plus cycle linkage and validation bookkeeping.
type DecisionRecord struct {
	ID               int64
	CycleID          int64
	Decision         model.Decision
	RawResponse      string // raw engine output, if any
	ValidationStatus string
}

// LLMCall is a persisted engine call (DESIGN §5): the full request context
// and raw response for exact replay.
type LLMCall struct {
	ID         int64
	CycleID    int64
	Model      string
	Request    string // JSON
	Response   string // raw
	DurationMS int64
	Error      string
	CreatedAt  time.Time
}

// Order is the broker-reported view of an order intent after submission or
// reconciliation (DESIGN §5) — one row per intent, upserted as that view
// changes.
type Order struct {
	ClientOrderID string
	BrokerOrderID string
	Status        string
	LotsExecuted  int64
	ExecutedPrice *model.Decimal
	RawStatus     string
	UpdatedAt     time.Time
}

// CashEntry is one cash_ledger movement. Ref is a free-form pointer (e.g. a
// fill id) whose shape depends on Reason.
type CashEntry struct {
	ID       int64
	TS       time.Time
	Delta    model.Decimal
	Currency string
	Reason   string
	Ref      string
}

// EquitySnapshot is one point on the equity curve.
type EquitySnapshot struct {
	ID          int64
	TS          time.Time
	Cash        model.Decimal
	MarketValue model.Decimal
	Total       model.Decimal
}

// Event is one structured log entry surfaced in the TUI/report (DESIGN §5).
type Event struct {
	ID      int64
	TS      time.Time
	Level   string
	Code    string
	Payload string // JSON, optional
}

// FeatureSnapshot is one computed indicator snapshot for an instrument.
type FeatureSnapshot struct {
	ID            int64
	InstrumentUID model.InstrumentUID
	AsOf          time.Time
	Payload       string // JSON
}

// FillRecord is a persisted fill: the engine-agnostic model.Fill plus two
// bookkeeping fields no domain package needs to see. LowFidelity is set once,
// at insert time, by whichever Executor priced the fill (true when the price
// came from the paper simulator's last-price fallback rather than a real
// bid/ask, DESIGN §7). RealizedPnL starts nil and is set later, by
// internal/portfolio via FillRepo.SetRealizedPnL, once a sell's PnL against
// the position's average price is known; it stays nil for a buy fill.
type FillRecord struct {
	model.Fill
	RealizedPnL *model.Decimal
	LowFidelity bool
}
