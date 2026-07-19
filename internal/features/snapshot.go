package features

import (
	"fmt"
	"math"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Params holds the lookback period for each indicator a Snapshot computes.
// All periods are configurable; DefaultParams returns the Phase 1 defaults.
type Params struct {
	SMAPeriod     int `json:"sma_period"`
	EMAFastPeriod int `json:"ema_fast_period"`
	EMASlowPeriod int `json:"ema_slow_period"`
	RSIPeriod     int `json:"rsi_period"`
	ATRPeriod     int `json:"atr_period"`
}

// DefaultParams returns the Phase 1 default indicator periods: SMA 20,
// EMA 20 & 50, RSI 14, ATR 14.
func DefaultParams() Params {
	return Params{
		SMAPeriod:     20,
		EMAFastPeriod: 20,
		EMASlowPeriod: 50,
		RSIPeriod:     14,
		ATRPeriod:     14,
	}
}

// requiredCandles returns the minimum candle count needed to produce every
// indicator in p without a half-warmed value: the largest of each
// indicator's own warm-up requirement (RSI and ATR need period+1 candles;
// SMA and EMA need period).
func (p Params) requiredCandles() int {
	required := p.SMAPeriod
	for _, n := range [...]int{p.EMAFastPeriod, p.EMASlowPeriod, p.RSIPeriod + 1, p.ATRPeriod + 1} {
		if n > required {
			required = n
		}
	}
	return required
}

// validate reports an error if any period is not positive. Checks run in a
// fixed field order so the error is deterministic when multiple periods are
// invalid.
func (p Params) validate() error {
	fields := [...]struct {
		name   string
		period int
	}{
		{"sma_period", p.SMAPeriod},
		{"ema_fast_period", p.EMAFastPeriod},
		{"ema_slow_period", p.EMASlowPeriod},
		{"rsi_period", p.RSIPeriod},
		{"atr_period", p.ATRPeriod},
	}
	for _, f := range fields {
		if f.period <= 0 {
			return fmt.Errorf("features: invalid params: %s must be positive, got %d", f.name, f.period)
		}
	}
	return nil
}

// EMATrend classifies the fast-EMA-vs-slow-EMA relationship of a Snapshot.
type EMATrend string

const (
	EMABullish EMATrend = "bullish" // fast EMA above slow EMA, beyond the deadband
	EMABearish EMATrend = "bearish" // fast EMA below slow EMA, beyond the deadband
	EMAFlat    EMATrend = "flat"    // fast and slow EMA within the deadband
)

// emaTrendDeadband is the half-width of the "flat" zone around equal EMAs,
// expressed as a fraction of the larger EMA magnitude. A mathematically flat
// price series does not yield exactly equal fast and slow EMAs: the two use
// different smoothing factors, so floating-point accumulation leaves a gap on
// the order of 1e-13 relative. A strict > / < comparison would read that noise
// as a bullish or bearish crossover. Any gap narrower than this fraction is
// treated as flat; a genuine crossover separates the EMAs by many orders of
// magnitude more, so real signals are unaffected.
const emaTrendDeadband = 1e-9

func classifyEMATrend(fast, slow float64) EMATrend {
	// Scale the deadband by the larger magnitude (floored at 1) so it is a
	// relative tolerance for normal prices and an absolute one near zero.
	tolerance := emaTrendDeadband * math.Max(math.Max(math.Abs(fast), math.Abs(slow)), 1)
	switch {
	case fast-slow > tolerance:
		return EMABullish
	case slow-fast > tolerance:
		return EMABearish
	default:
		return EMAFlat
	}
}

// RSIZone classifies a Snapshot's RSI value into the standard overbought /
// oversold / neutral zones (30 / 70 thresholds).
type RSIZone string

const (
	RSIOversold   RSIZone = "oversold"   // RSI <= 30
	RSIOverbought RSIZone = "overbought" // RSI >= 70
	RSINeutral    RSIZone = "neutral"    // 30 < RSI < 70
)

const (
	rsiOversoldThreshold   = 30.0
	rsiOverboughtThreshold = 70.0
)

func classifyRSIZone(rsi float64) RSIZone {
	switch {
	case rsi <= rsiOversoldThreshold:
		return RSIOversold
	case rsi >= rsiOverboughtThreshold:
		return RSIOverbought
	default:
		return RSINeutral
	}
}

// Snapshot is the point-in-time feature set for one instrument/interval,
// computed strictly from completed candles up to AsOf. This exact shape is
// what gets stored in feature_snapshots.payload (DESIGN.md §5) and later
// embedded in decision-cycle context (DESIGN.md §6), so its JSON field
// names are a stable contract, not an implementation detail.
type Snapshot struct {
	UID       model.InstrumentUID  `json:"uid"`
	Interval  model.CandleInterval `json:"interval"`
	AsOf      time.Time            `json:"as_of"`
	LastClose model.Decimal        `json:"last_close"`
	Volume    int64                `json:"volume"`

	SMA     float64 `json:"sma"`
	EMAFast float64 `json:"ema_fast"`
	EMASlow float64 `json:"ema_slow"`
	RSI     float64 `json:"rsi"`
	ATR     float64 `json:"atr"`

	EMATrend EMATrend `json:"ema_trend"`
	RSIZone  RSIZone  `json:"rsi_zone"`

	Params Params `json:"params"`
}

// Build assembles a Snapshot for uid/interval from candles. candles must be
// ordered oldest→newest and contain only completed bars — see the package
// doc comment for the full as-of discipline contract; Build does not
// validate ordering or completeness itself.
//
// Build returns ErrInsufficientData when candles has fewer bars than params
// requires to produce every indicator without a half-warmed value; no
// partial Snapshot is ever returned in that case. AsOf, LastClose, and
// Volume are taken from the last candle in the slice.
func Build(uid model.InstrumentUID, interval model.CandleInterval, candles []model.Candle, params Params) (Snapshot, error) {
	if err := params.validate(); err != nil {
		return Snapshot{}, err
	}
	// Every candle must belong to the instrument/interval the snapshot claims:
	// an upstream assembly or grouping error that mixed in another instrument's
	// bars would otherwise attach the wrong indicators to an order decision.
	for i := range candles {
		if candles[i].InstrumentUID != uid {
			return Snapshot{}, ErrCandleMismatch{Index: i, Field: "instrument_uid", Want: string(uid), Got: string(candles[i].InstrumentUID)}
		}
		if candles[i].Interval != interval {
			return Snapshot{}, ErrCandleMismatch{Index: i, Field: "interval", Want: interval.String(), Got: candles[i].Interval.String()}
		}
	}
	if required := params.requiredCandles(); len(candles) < required {
		return Snapshot{}, ErrInsufficientData{Required: required, Got: len(candles)}
	}

	sma, err := SMA(candles, params.SMAPeriod)
	if err != nil {
		return Snapshot{}, err
	}
	emaFast, err := EMA(candles, params.EMAFastPeriod)
	if err != nil {
		return Snapshot{}, err
	}
	emaSlow, err := EMA(candles, params.EMASlowPeriod)
	if err != nil {
		return Snapshot{}, err
	}
	rsi, err := RSI(candles, params.RSIPeriod)
	if err != nil {
		return Snapshot{}, err
	}
	atr, err := ATR(candles, params.ATRPeriod)
	if err != nil {
		return Snapshot{}, err
	}

	last := candles[len(candles)-1]
	return Snapshot{
		UID:       uid,
		Interval:  interval,
		AsOf:      last.TS,
		LastClose: last.Close,
		Volume:    last.Volume,
		SMA:       sma,
		EMAFast:   emaFast,
		EMASlow:   emaSlow,
		RSI:       rsi,
		ATR:       atr,
		EMATrend:  classifyEMATrend(emaFast, emaSlow),
		RSIZone:   classifyRSIZone(rsi),
		Params:    params,
	}, nil
}
