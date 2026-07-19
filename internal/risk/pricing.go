package risk

import "github.com/Dronnn/invest-robot/internal/model"

// priceForBuy returns the price used to cost and value a candidate buy
// decision: the decision's own limit price for a limit order, otherwise the
// market price — ask when the quote has one, else last. ok is false when no
// usable price could be determined — a market order with no positive ask and
// no positive last in the quote, or a limit order whose limit price is not
// strictly positive — in which case the caller must treat the buy
// conservatively (strip, never skip the check). A non-positive price is
// rejected rather than used: an untrusted decision could carry a zero or
// negative limit that would otherwise produce a zero/negative notional and
// slip past every monetary limit.
func priceForBuy(d model.Decision, q model.Quote) (price model.Decimal, ok bool) {
	if d.OrderType == model.OrderLimit && d.LimitPrice != nil {
		if d.LimitPrice.Sign() <= 0 {
			return model.Decimal{}, false
		}
		return *d.LimitPrice, true
	}
	if q.Ask.Sign() > 0 {
		return q.Ask, true
	}
	if q.Last.Sign() > 0 {
		return q.Last, true
	}
	return model.Decimal{}, false
}

// exposurePrice returns the price used to mark an existing position or a
// pending intent for one instrument when computing an exposure baseline
// (rules 5-6): the position's own recorded mark, Positions[uid].LastPrice,
// when it is set, else the latest quote's last price. ok is false when
// neither is available, meaning a non-zero quantity for this instrument
// cannot be valued and the baseline cannot be proven safe.
func exposurePrice(uid model.InstrumentUID, state State) (model.Decimal, bool) {
	if pos, found := state.Positions[uid]; found && pos.LastPrice.Sign() > 0 {
		return pos.LastPrice, true
	}
	if q, found := state.Quotes[uid]; found && q.Last.Sign() > 0 {
		return q.Last, true
	}
	return model.Decimal{}, false
}

// notionalOf returns qtyLots worth of lot shares at price, or ok=false when
// the inputs cannot be turned into a trustworthy notional: a non-positive
// quantity, lot or price, or a lots-to-shares / price multiplication that
// would overflow. Every caller treats ok=false as "cannot prove this safe"
// and fails closed (strips the action, or forces a zero budget) rather than
// admit an unpriced, zero or wrapped-around notional. In particular the raw
// qtyLots*lot product is computed with an overflow guard: a value like
// MaxInt64*2 wraps to a small or negative int64, which would otherwise sail
// through every exposure and cash check as a tiny notional.
func notionalOf(qtyLots, lot int64, price model.Decimal) (model.Decimal, bool) {
	shares, ok := sharesFor(qtyLots, lot)
	if !ok || price.Sign() <= 0 {
		return model.Decimal{}, false
	}
	n, err := price.MulInt(shares)
	if err != nil {
		return model.Decimal{}, false
	}
	return n, true
}

// sharesFor returns qtyLots*lot as a positive share count, or ok=false when
// either input is non-positive or the product overflows int64. The overflow
// check divides the product back out: a wrapped result cannot recover the
// original quantity, so it is rejected rather than trusted.
func sharesFor(qtyLots, lot int64) (int64, bool) {
	if qtyLots <= 0 || lot <= 0 {
		return 0, false
	}
	shares := qtyLots * lot
	if shares/lot != qtyLots {
		return 0, false
	}
	return shares, true
}

// maxWithinBudget returns the largest k in [0, cap] for which fits(k) is
// true, assuming fits is monotonically non-increasing in k (every cost
// function in this package only grows with quantity). It binary-searches
// using only the boolean comparisons model.Decimal exposes, since Decimal
// has no division operator — the search is exact to the lot because it
// tests each candidate quantity with the same arithmetic the rest of the
// package uses to cost it, rather than rounding a division.
func maxWithinBudget(cap int64, fits func(k int64) bool) int64 {
	if cap <= 0 || !fits(1) {
		return 0
	}
	if fits(cap) {
		return cap
	}
	lo, hi := int64(1), cap
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
