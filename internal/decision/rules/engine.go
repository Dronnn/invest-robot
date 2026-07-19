package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/decision"
	"github.com/Dronnn/invest-robot/internal/model"
)

// version is Engine.Version()'s value. Bump it whenever the strategy logic
// changes in a way that would change a past decision's replay.
const version = "rules/v1"

// Engine is the deterministic rules-based decision.Engine: long-only
// trend-following, entering on an EMA-cross-up with RSI confirmation, exiting
// on an EMA-cross-down or an RSI overbought reading, sized by ATR risk per
// share against a fixed fraction of equity.
type Engine struct {
	params Params
	clock  clock.Clock
}

var _ decision.Engine = (*Engine)(nil)

// New builds an Engine with params (validated) and clk, which is used only to
// time Decide's own duration (Meta.DurationMS) — never to make a decision.
// clk defaults to clock.Real() when nil.
func New(params Params, clk clock.Clock) (*Engine, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}
	if clk == nil {
		clk = clock.Real()
	}
	return &Engine{params: params, clock: clk}, nil
}

// Name implements decision.Engine.
func (e *Engine) Name() string { return "rules" }

// Version implements decision.Engine.
func (e *Engine) Version() string { return version }

// Decide implements decision.Engine. It is a pure function of req: the same
// req always produces a byte-identical Response (see golden_test.go).
func (e *Engine) Decide(ctx context.Context, req decision.Request) (decision.Response, decision.Meta, error) {
	if err := ctx.Err(); err != nil {
		return decision.Response{}, decision.Meta{}, err
	}
	start := e.clock.Now()

	positions := make(map[model.InstrumentUID]decision.PositionView, len(req.Portfolio.Positions))
	for _, p := range req.Portfolio.Positions {
		positions[p.UID] = p
	}

	actions := make([]model.Decision, 0, len(req.Instruments))
	var skipped []string

	for _, instr := range req.Instruments {
		if instr.DataFreshness > e.params.MaxDataAge {
			skipped = append(skipped, fmt.Sprintf("%s: stale data (%s > %s)", instr.Ticker, instr.DataFreshness, e.params.MaxDataAge))
			continue
		}

		pos, hasPosition := positions[instr.UID]
		hasPosition = hasPosition && pos.Qty > 0
		snap := instr.Features

		switch {
		case !hasPosition && snap.EMAFast > snap.EMASlow && snap.RSI > e.params.RSIEntryLow && snap.RSI < e.params.RSIEntryHigh:
			dec, sized, err := e.buildEntry(req, instr)
			if err != nil {
				return decision.Response{}, decision.Meta{}, err
			}
			if sized {
				actions = append(actions, dec)
			} else {
				actions = append(actions, e.buildHold(instr, "entry signal sized to zero"))
			}
		case hasPosition && (snap.EMAFast < snap.EMASlow || snap.RSI > e.params.RSIExitHigh):
			actions = append(actions, e.buildExit(instr))
		default:
			actions = append(actions, e.buildHold(instr, ""))
		}
	}

	resp := decision.Response{
		Actions: actions,
		Notes:   strings.Join(skipped, "; "),
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		return decision.Response{}, decision.Meta{}, fmt.Errorf("rules: marshal response: %w", err)
	}

	meta := decision.Meta{
		DurationMS: e.clock.Now().Sub(start).Milliseconds(),
		Raw:        raw,
	}
	return resp, meta, nil
}

// buildEntry sizes and constructs a market buy for instr. The second return
// value is false when the signal fired but sizing rounded to zero lots (too
// little risk budget, cash, or notional headroom to buy even one lot) — the
// caller falls back to an explicit hold in that case.
func (e *Engine) buildEntry(req decision.Request, instr decision.InstrumentContext) (model.Decision, bool, error) {
	snap := instr.Features
	price := snap.LastClose.Float64()
	if price <= 0 || instr.Lot <= 0 {
		return model.Decision{}, false, nil
	}

	riskBudget, err := req.Portfolio.Equity.MulBps(e.params.RiskFractionBps)
	if err != nil {
		return model.Decision{}, false, fmt.Errorf("rules: risk budget for %s: %w", instr.UID, err)
	}

	// Float/Decimal boundary: ATR arrives as a float64 from features (all
	// indicator math is float64 there). risk_per_share is computed in
	// float64 because model.Decimal exposes no division operator, and the
	// budget/lot sizing division below therefore runs in float64 too. Only
	// the resulting integer lot count re-enters Decimal money arithmetic,
	// for the notional/cash cap checks that follow.
	riskPerShare := snap.ATR * e.params.ATRMultiplier
	if riskPerShare <= 0 {
		return model.Decision{}, false, nil // degenerate ATR (flat market): no sizing signal
	}
	lots := int64(math.Floor(riskBudget.Float64() / riskPerShare / float64(instr.Lot)))
	if lots <= 0 {
		return model.Decision{}, false, nil
	}

	if maxNotional := req.Limits.MaxPositionNotional; maxNotional.Sign() > 0 {
		if maxLots := int64(math.Floor(maxNotional.Float64() / (price * float64(instr.Lot)))); maxLots < lots {
			lots = maxLots
		}
	}
	if cashLots := int64(math.Floor(req.Portfolio.Cash.Float64() / (price * float64(instr.Lot)))); cashLots < lots {
		lots = cashLots
	}
	if lots <= 0 {
		return model.Decision{}, false, nil
	}

	return model.Decision{
		InstrumentUID: instr.UID,
		Action:        model.ActionBuy,
		Quantity:      lots,
		OrderType:     model.OrderMarket,
		TimeInForce:   model.TIFDay,
		Rationale: fmt.Sprintf(
			"enter: ema_fast %.4f > ema_slow %.4f, rsi %.2f in (%.0f,%.0f); risk_per_share=%.4f, sized %d lot(s)",
			snap.EMAFast, snap.EMASlow, snap.RSI, e.params.RSIEntryLow, e.params.RSIEntryHigh, riskPerShare, lots,
		),
		Confidence: e.confidence(snap.RSI),
	}, true, nil
}

// buildExit constructs a market close for instr's existing position.
func (e *Engine) buildExit(instr decision.InstrumentContext) model.Decision {
	snap := instr.Features
	reason := fmt.Sprintf("ema_fast %.4f < ema_slow %.4f", snap.EMAFast, snap.EMASlow)
	if snap.EMAFast >= snap.EMASlow {
		reason = fmt.Sprintf("rsi %.2f > %.0f", snap.RSI, e.params.RSIExitHigh)
	}
	return model.Decision{
		InstrumentUID: instr.UID,
		Action:        model.ActionClose,
		OrderType:     model.OrderMarket,
		TimeInForce:   model.TIFDay,
		Rationale:     fmt.Sprintf("exit: %s", reason),
		Confidence:    e.confidence(snap.RSI),
	}
}

// buildHold constructs an explicit hold for instr, optionally noting why (an
// entry signal that sized to zero, for example).
func (e *Engine) buildHold(instr decision.InstrumentContext, note string) model.Decision {
	snap := instr.Features
	rationale := fmt.Sprintf("hold: ema_fast %.4f, ema_slow %.4f, rsi %.2f", snap.EMAFast, snap.EMASlow, snap.RSI)
	if note != "" {
		rationale += " (" + note + ")"
	}
	return model.Decision{
		InstrumentUID: instr.UID,
		Action:        model.ActionHold,
		OrderType:     model.OrderMarket,
		TimeInForce:   model.TIFDay,
		Rationale:     rationale,
		Confidence:    e.confidence(snap.RSI),
	}
}

// confidence maps an RSI reading to a deterministic confidence score:
// ConfidenceBase plus a bonus proportional to RSI's distance from the
// neutral midpoint (50), capped at ConfidenceRSIBonusMax and clamped to
// [0,1].
func (e *Engine) confidence(rsi float64) float64 {
	bonus := e.params.ConfidenceRSIBonusMax * math.Min(1, math.Abs(rsi-50)/50)
	c := e.params.ConfidenceBase + bonus
	switch {
	case c < 0:
		return 0
	case c > 1:
		return 1
	default:
		return c
	}
}
