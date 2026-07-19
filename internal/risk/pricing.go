package risk

import "github.com/Dronnn/invest-robot/internal/model"

// priceForBuy returns the price used to cost and value a candidate buy
// decision: the decision's own limit price for a limit order, otherwise the
// market price — ask when the quote has one, else last. ok is false when no
// price could be determined (a market order with no ask and no last in the
// quote), in which case the caller must treat the buy conservatively
// (strip, never skip the check).
func priceForBuy(d model.Decision, q model.Quote) (price model.Decimal, ok bool) {
	if d.OrderType == model.OrderLimit && d.LimitPrice != nil {
		return *d.LimitPrice, true
	}
	if !q.Ask.IsZero() {
		return q.Ask, true
	}
	if !q.Last.IsZero() {
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
	if pos, found := state.Positions[uid]; found && !pos.LastPrice.IsZero() {
		return pos.LastPrice, true
	}
	if q, found := state.Quotes[uid]; found && !q.Last.IsZero() {
		return q.Last, true
	}
	return model.Decimal{}, false
}

// notionalOf returns qtyLots worth of lot shares at price, or ok=false if
// the multiplication would overflow model.Decimal. qtyLots is taken as a
// magnitude (its sign is ignored) since exposure is about the size of a
// holding, not its direction — Phase 1 positions are never short, but this
// keeps a stray negative value from silently widening a budget instead of
// consuming it.
func notionalOf(qtyLots, lot int64, price model.Decimal) (model.Decimal, bool) {
	if qtyLots == 0 || lot == 0 {
		return model.Decimal{}, true
	}
	if qtyLots < 0 {
		qtyLots = -qtyLots
	}
	shares := qtyLots * lot
	n, err := price.MulInt(shares)
	if err != nil {
		return model.Decimal{}, false
	}
	return n, true
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
