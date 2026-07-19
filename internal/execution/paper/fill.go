package paper

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Dronnn/invest-robot/internal/model"
)

// priceFill computes the fill price for a resting order against a quote per
// DESIGN §7. It returns ok=false when the order cannot fill on this observation
// (no usable price, or a limit that does not cross) and an error only for an
// arithmetic overflow. lowFidelity is true when the price came from the
// last-price fallback rather than a real bid/ask.
//
// Conventions (all from DESIGN §7):
//   - market buy fills at ask + adverse slippage; market sell at bid − slippage.
//   - limit buy fills when ask ≤ limit, at min(ask, limit) — a marketable limit
//     takes the better current price; limit sell fills when bid ≥ limit, at
//     max(bid, limit). Limits carry no slippage.
//   - the price is tick-aligned with Floor for buys and Ceil for sells, so a
//     buy never rounds above and a sell never below the crossing price.
func (s *Simulator) priceFill(side model.Side, typ model.OrderType, limit *model.Decimal, q model.Quote, tick model.Decimal) (price model.Decimal, lowFidelity bool, ok bool, err error) {
	switch side {
	case model.SideBuy:
		base, lowFi, have := referenceBuy(q)
		if !have {
			return model.Decimal{}, false, false, nil
		}
		var raw model.Decimal
		if typ == model.OrderLimit {
			if limit == nil || base.Cmp(*limit) > 0 {
				return model.Decimal{}, false, false, nil // does not cross
			}
			raw = base // min(base, limit) == base, since base ≤ limit
		} else {
			if raw, err = adverse(base, s.slippageBps, true); err != nil {
				return model.Decimal{}, false, false, err
			}
		}
		aligned, err := raw.RoundToIncrement(tick, model.Floor)
		if err != nil {
			return model.Decimal{}, false, false, err
		}
		return aligned, lowFi, true, nil

	case model.SideSell:
		base, lowFi, have := referenceSell(q)
		if !have {
			return model.Decimal{}, false, false, nil
		}
		var raw model.Decimal
		if typ == model.OrderLimit {
			if limit == nil || base.Cmp(*limit) < 0 {
				return model.Decimal{}, false, false, nil // does not cross
			}
			raw = base // max(base, limit) == base, since base ≥ limit
		} else {
			if raw, err = adverse(base, s.slippageBps, false); err != nil {
				return model.Decimal{}, false, false, err
			}
		}
		aligned, err := raw.RoundToIncrement(tick, model.Ceil)
		if err != nil {
			return model.Decimal{}, false, false, err
		}
		return aligned, lowFi, true, nil

	default:
		return model.Decimal{}, false, false, fmt.Errorf("paper: unknown side %q", side)
	}
}

// referenceBuy is the base price a buy prices off: the ask if known, else the
// last price (low fidelity). have is false when neither is available.
func referenceBuy(q model.Quote) (base model.Decimal, lowFidelity, have bool) {
	if q.Ask.Sign() > 0 {
		return q.Ask, false, true
	}
	if q.Last.Sign() > 0 {
		return q.Last, true, true
	}
	return model.Decimal{}, false, false
}

// referenceSell is the base price a sell prices off: the bid if known, else the
// last price (low fidelity).
func referenceSell(q model.Quote) (base model.Decimal, lowFidelity, have bool) {
	if q.Bid.Sign() > 0 {
		return q.Bid, false, true
	}
	if q.Last.Sign() > 0 {
		return q.Last, true, true
	}
	return model.Decimal{}, false, false
}

// adverse applies bps of slippage to base against the trader: added for a buy,
// subtracted for a sell. Zero bps returns base unchanged.
func adverse(base model.Decimal, bps int64, buy bool) (model.Decimal, error) {
	if bps == 0 {
		return base, nil
	}
	slip, err := base.MulBps(bps)
	if err != nil {
		return model.Decimal{}, err
	}
	if buy {
		return base.Add(slip)
	}
	return base.Sub(slip)
}

// commission is the fee for a fill: notional × rate, where notional is the
// per-share price times the total shares (lots × lot size) and rate is the
// configured commissionNum/commissionDen ratio, rounded half away from zero (the
// money-rounding mode).
func commission(price model.Decimal, lots, lot, rateNum, rateDen int64) (model.Decimal, error) {
	if rateNum == 0 {
		return model.Decimal{}, nil
	}
	notional, err := price.MulInt(lots * lot)
	if err != nil {
		return model.Decimal{}, fmt.Errorf("paper: fill notional: %w", err)
	}
	fee, err := notional.MulRat(rateNum, rateDen, model.RoundHalfAwayFromZero)
	if err != nil {
		return model.Decimal{}, fmt.Errorf("paper: commission: %w", err)
	}
	return fee, nil
}

// parseRate converts a decimal commission-rate string into an exact
// numerator/denominator pair (denominator a power of ten), so a fee is computed
// as notional*num/den with big.Int intermediates and no float rounding. It
// validates through model.ParseDecimal first, so the same inputs config accepts
// are accepted here.
func parseRate(s string) (num, den int64, err error) {
	if _, perr := model.ParseDecimal(s); perr != nil {
		return 0, 0, fmt.Errorf("paper: commission rate %q: %w", s, perr)
	}
	body := s
	neg := false
	switch body[0] {
	case '+':
		body = body[1:]
	case '-':
		neg = true
		body = body[1:]
	}
	intPart, fracPart := body, ""
	if dot := strings.IndexByte(body, '.'); dot >= 0 {
		intPart, fracPart = body[:dot], body[dot+1:]
	}
	// ParseDecimal guarantees digits-only parts of at most nine fractional
	// digits whose combined value fits the int64 mantissa, so digits fits int64.
	digits := intPart + fracPart
	num, err = strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("paper: commission rate %q out of range: %w", s, err)
	}
	if neg {
		num = -num
	}
	den = 1
	for range fracPart {
		den *= 10
	}
	return num, den, nil
}
