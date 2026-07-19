package rules

import (
	"fmt"
	"time"
)

// Params configures the rules engine's strategy. All fields are tunable;
// DefaultParams returns the Phase 1 defaults.
type Params struct {
	// ATRMultiplier scales ATR14 into a per-share risk amount: risk_per_share
	// = ATR * ATRMultiplier.
	ATRMultiplier float64 `json:"atr_multiplier"`
	// RiskFractionBps is the fraction of Equity risked per trade, in basis
	// points (100 = 1%): risk_budget = Equity * RiskFractionBps/10000.
	RiskFractionBps int64 `json:"risk_fraction_bps"`

	// RSIEntryLow/RSIEntryHigh bound the open RSI interval an entry requires
	// (default (50, 70)).
	RSIEntryLow  float64 `json:"rsi_entry_low"`
	RSIEntryHigh float64 `json:"rsi_entry_high"`
	// RSIExitHigh is the RSI level above which an open position exits
	// regardless of the EMA trend (default 80).
	RSIExitHigh float64 `json:"rsi_exit_high"`

	// MaxDataAge is the freshness ceiling: instruments whose
	// InstrumentContext.DataFreshness exceeds this are skipped for the
	// cycle rather than decided on stale data.
	MaxDataAge time.Duration `json:"max_data_age"`

	// ConfidenceBase and ConfidenceRSIBonusMax define the confidence
	// mapping: confidence = ConfidenceBase + ConfidenceRSIBonusMax *
	// min(1, |RSI-50|/50), clamped to [0,1].
	ConfidenceBase        float64 `json:"confidence_base"`
	ConfidenceRSIBonusMax float64 `json:"confidence_rsi_bonus_max"`
}

// DefaultParams returns the Phase 1 default strategy parameters.
func DefaultParams() Params {
	return Params{
		ATRMultiplier:         2,
		RiskFractionBps:       100, // 1%
		RSIEntryLow:           50,
		RSIEntryHigh:          70,
		RSIExitHigh:           80,
		MaxDataAge:            30 * time.Minute,
		ConfidenceBase:        0.6,
		ConfidenceRSIBonusMax: 0.3,
	}
}

// validate reports an error if p is not internally consistent. Checks run in
// a fixed order so the error is deterministic when multiple fields are
// invalid.
func (p Params) validate() error {
	if p.ATRMultiplier <= 0 {
		return fmt.Errorf("rules: atr_multiplier must be positive, got %v", p.ATRMultiplier)
	}
	if p.RiskFractionBps <= 0 {
		return fmt.Errorf("rules: risk_fraction_bps must be positive, got %d", p.RiskFractionBps)
	}
	if p.RSIEntryLow < 0 || p.RSIEntryHigh > 100 || p.RSIEntryLow >= p.RSIEntryHigh {
		return fmt.Errorf("rules: rsi_entry_low/rsi_entry_high must satisfy 0 <= low < high <= 100, got [%v, %v]", p.RSIEntryLow, p.RSIEntryHigh)
	}
	if p.RSIExitHigh <= p.RSIEntryHigh || p.RSIExitHigh > 100 {
		return fmt.Errorf("rules: rsi_exit_high must be in (rsi_entry_high, 100], got %v (entry high %v)", p.RSIExitHigh, p.RSIEntryHigh)
	}
	if p.MaxDataAge <= 0 {
		return fmt.Errorf("rules: max_data_age must be positive, got %v", p.MaxDataAge)
	}
	if p.ConfidenceBase < 0 || p.ConfidenceBase > 1 {
		return fmt.Errorf("rules: confidence_base must be in [0,1], got %v", p.ConfidenceBase)
	}
	if p.ConfidenceRSIBonusMax < 0 {
		return fmt.Errorf("rules: confidence_rsi_bonus_max must not be negative, got %v", p.ConfidenceRSIBonusMax)
	}
	return nil
}
