package risk

import (
	"strings"

	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/model"
)

// Check evaluates actions against limits and state, applying the eight
// rules documented in the package comment in order. Every rule after the
// first sees the actions as the rules before it left them: a decision
// stripped by one rule is invisible to every rule that follows, and a
// decision shrunk by one rule presents its already-shrunk quantity to the
// next.
func Check(actions []model.Decision, state State, limits config.RiskConfig) Result {
	entries := make([]*entry, len(actions))
	for i, d := range actions {
		entries[i] = &entry{idx: i, dec: d, live: true}
	}

	var adjustments []Adjustment

	maxDailyLoss := parseLimitOrZero(limits.MaxDailyLoss)
	halted := applyKillSwitch(entries, state, maxDailyLoss, &adjustments)

	applyOperationalHalt(entries, state, &adjustments)
	applyAllowlist(entries, state, limits.Allowlist, &adjustments)
	applyCurrencyMismatch(entries, state, &adjustments)
	applyOrderCap(entries, limits.MaxOrdersPerCycle, RuleMaxOrdersPerCycle,
		"per-cycle order cap reached", &adjustments)
	applyOrderCap(entries, limits.MaxOrdersPerDay-state.OrdersToday, RuleMaxOrdersPerDay,
		"daily order cap reached", &adjustments)

	maxPositionNotional := parseLimitOrZero(limits.MaxPositionNotional)
	applyPositionNotional(entries, state, maxPositionNotional, &adjustments)

	maxTotalExposure := parseLimitOrZero(limits.MaxTotalExposure)
	applyTotalExposure(entries, state, maxTotalExposure, &adjustments)

	cashFloor := parseLimitOrZero(limits.CashFloor)
	applyCashFloor(entries, state, cashFloor, &adjustments)

	applyOversell(entries, state, &adjustments)

	allowed := make([]model.Decision, 0, len(entries))
	for _, e := range entries {
		if e.live {
			allowed = append(allowed, e.dec)
		}
	}

	return Result{Allowed: allowed, Adjustments: adjustments, Halted: halted}
}

// entry is one action moving through the rule pipeline: dec holds its
// current (possibly already shrunk) value, and live is false once some rule
// has stripped it. idx is its position in the original actions slice,
// carried through to Adjustment.Index regardless of how many entries ahead
// of it get stripped.
type entry struct {
	idx  int
	dec  model.Decision
	live bool
}

// strip removes a live entry and records why. A no-op on an already-dead
// entry (defensive: the pipeline never revisits a dead entry, but a rule
// bug that did would silently double-record otherwise).
func strip(e *entry, rule Rule, reason string, adjustments *[]Adjustment) {
	if !e.live {
		return
	}
	*adjustments = append(*adjustments, Adjustment{
		Index:         e.idx,
		InstrumentUID: e.dec.InstrumentUID,
		Rule:          rule,
		Original:      e.dec,
		Adjusted:      nil,
		Reason:        reason,
	})
	e.live = false
}

// shrink reduces a live entry's quantity to newQty and records the change.
// newQty <= 0 strips instead. A newQty equal to the entry's current
// quantity is a no-op (nothing changed, nothing to record).
func shrink(e *entry, newQty int64, rule Rule, reason string, adjustments *[]Adjustment) {
	if !e.live || newQty == e.dec.Quantity {
		return
	}
	if newQty <= 0 {
		strip(e, rule, reason, adjustments)
		return
	}
	original := e.dec
	e.dec.Quantity = newQty
	adjusted := e.dec
	*adjustments = append(*adjustments, Adjustment{
		Index:         e.idx,
		InstrumentUID: e.dec.InstrumentUID,
		Rule:          rule,
		Original:      original,
		Adjusted:      &adjusted,
		Reason:        reason,
	})
}

// isExit reports whether an action reduces or closes a position — the
// actions the allowlist and kill-switch rules must never block, since an
// existing position must always be exitable.
func isExit(a model.Action) bool {
	return a == model.ActionSell || a == model.ActionClose
}

// isOrder reports whether an action produces an order (consumes an
// order-cap slot); hold does not.
func isOrder(a model.Action) bool {
	return a == model.ActionBuy || a == model.ActionSell || a == model.ActionClose
}

// parseLimitOrZero parses a config.RiskConfig decimal-string limit, treating
// a parse failure as a zero limit — the most restrictive interpretation.
// Check trusts config.Validate() to have already rejected malformed limit
// strings in the normal startup path; this is the fail-closed fallback for
// a config.RiskConfig built or mutated some other way.
func parseLimitOrZero(s string) model.Decimal {
	d, err := model.ParseDecimal(s)
	if err != nil {
		return model.Decimal{}
	}
	return d
}

// applyKillSwitch is rule 1. It computes today's loss magnitude from
// state.DayPnL (0 when DayPnL is not negative) and halts the cycle when
// that magnitude is at least maxDailyLoss. A configured limit of exactly
// "0" therefore has zero loss tolerance: the switch engages every cycle,
// even with no loss yet, since a zero limit admits no non-negative
// magnitude below it. There is no config-level "unlimited" sentinel in this
// design (DESIGN.md §11) — an operator who wants the kill switch inert
// leaves max_daily_loss comfortably large, not zero.
//
// When halted, every live entry that is not an exit (sell/close) is
// stripped, including hold: DESIGN.md's flatten-only mode is read literally
// as "only sell/close actions pass".
func applyKillSwitch(entries []*entry, state State, maxDailyLoss model.Decimal, adjustments *[]Adjustment) bool {
	lossMagnitude := model.Decimal{}
	if state.DayPnL.Sign() < 0 {
		lossMagnitude = state.DayPnL.Neg()
	}
	halted := lossMagnitude.Cmp(maxDailyLoss) >= 0
	if !halted {
		return false
	}
	for _, e := range entries {
		if !e.live || isExit(e.dec.Action) {
			continue
		}
		strip(e, RuleDailyLossKillSwitch, "daily loss kill switch engaged: flatten-only mode", adjustments)
	}
	return true
}

// applyAllowlist is rule 2. An empty allowlist means no additional
// restriction beyond the universe (robot.example.toml's documented
// default); a non-empty allowlist strips any buy whose instrument matches
// neither entry form the config comments allow for [universe].instruments —
// UID or ticker. Sells and closes always pass regardless of allowlist
// membership.
func applyAllowlist(entries []*entry, state State, allowlist []string, adjustments *[]Adjustment) {
	if len(allowlist) == 0 {
		return
	}
	allow := make(map[string]bool, len(allowlist))
	for _, a := range allowlist {
		allow[a] = true
	}
	for _, e := range entries {
		if !e.live || e.dec.Action != model.ActionBuy {
			continue
		}
		uid := e.dec.InstrumentUID
		ticker := ""
		if instr, found := state.Instruments[uid]; found {
			ticker = instr.Ticker
		}
		if allow[string(uid)] || (ticker != "" && allow[ticker]) {
			continue
		}
		strip(e, RuleAllowlist, "instrument not in configured allowlist", adjustments)
	}
}

// applyOperationalHalt strips every new buy when an operational halt is
// latched (state.Halted), leaving sells, closes and holds untouched: a halt
// stops the robot opening or adding to positions but must never trap an
// existing one, which stays exitable. The halt is a durable, operator-cleared
// state set outside this package (e.g. a fill settling below the cash floor);
// risk only reads it.
func applyOperationalHalt(entries []*entry, state State, adjustments *[]Adjustment) {
	if !state.Halted {
		return
	}
	for _, e := range entries {
		if !e.live || e.dec.Action != model.ActionBuy {
			continue
		}
		strip(e, RuleOperationalHalt, "operational halt engaged: new buys blocked until cleared", adjustments)
	}
}

// applyCurrencyMismatch strips any order (buy/sell/close) on an instrument
// whose currency differs from state.BaseCurrency. Every notional and cash
// figure in this package is single-currency; an instrument settled in another
// currency cannot be summed or compared against the base-currency limits
// without silently mixing incommensurable amounts (booking a USD 100 fill as
// RUB 100). The comparison is case-insensitive. An empty BaseCurrency disables
// the check; an instrument with no known currency is left for the pricing rules
// to handle as missing metadata, so this rule only ever acts on a known,
// mismatched currency. Exits are stripped too — Phase 1 never enters a
// non-base position (the buy is stripped here), and a mismatched exit cannot be
// valued in the base currency any more safely than an entry.
func applyCurrencyMismatch(entries []*entry, state State, adjustments *[]Adjustment) {
	if state.BaseCurrency == "" {
		return
	}
	for _, e := range entries {
		if !e.live || !isOrder(e.dec.Action) {
			continue
		}
		instr, ok := state.Instruments[e.dec.InstrumentUID]
		if !ok || instr.Currency == "" {
			continue // missing metadata: handled by the pricing rules
		}
		if !strings.EqualFold(instr.Currency, state.BaseCurrency) {
			strip(e, RuleCurrencyMismatch, "instrument currency "+instr.Currency+" differs from base currency "+state.BaseCurrency, adjustments)
		}
	}
}

// applyOrderCap backs rules 3 and 4: it keeps the first cap live,
// order-producing entries (buy/sell/close, in original order) and strips
// the rest. hold entries are not order-producing and are neither counted
// against cap nor touched. A negative cap (e.g. the day's budget already
// exhausted by OrdersToday) is clamped to zero.
func applyOrderCap(entries []*entry, cap int, rule Rule, reason string, adjustments *[]Adjustment) {
	if cap < 0 {
		cap = 0
	}
	kept := 0
	for _, e := range entries {
		if !e.live || !isOrder(e.dec.Action) {
			continue
		}
		if kept < cap {
			kept++
			continue
		}
		strip(e, rule, reason, adjustments)
	}
}

// applyPositionNotional is rule 5. For each live buy, in order, it checks
// existing position notional + pending buy-intent notional + every buy
// already committed to the same instrument earlier in this same call +
// this buy, against maxPositionNotional, shrinking (whole lots, floor) or
// stripping as needed. Existing position and pending-intent notional are
// priced via exposurePrice (the position's own mark, else the latest
// quote); the candidate buy itself is priced via priceForBuy (its own limit
// price, else ask/last). Any missing instrument metadata, missing price, or
// arithmetic overflow along the way is treated as "cannot prove this buy is
// safe" and the buy is stripped under this rule.
func applyPositionNotional(entries []*entry, state State, maxPositionNotional model.Decimal, adjustments *[]Adjustment) {
	committed := make(map[model.InstrumentUID]model.Decimal)
	for _, e := range entries {
		if !e.live || e.dec.Action != model.ActionBuy {
			continue
		}
		uid := e.dec.InstrumentUID

		instr, hasInstr := state.Instruments[uid]
		if !hasInstr {
			strip(e, RuleMaxPositionNotional, "no instrument metadata for "+string(uid), adjustments)
			continue
		}
		price, hasPrice := priceForBuy(e.dec, state.Quotes[uid])
		if !hasPrice {
			strip(e, RuleMaxPositionNotional, "no price available to value buy for "+string(uid), adjustments)
			continue
		}

		used, ok := existingExposure(uid, instr.Lot, state)
		if ok {
			if c, found := committed[uid]; found {
				if v, err := used.Add(c); err == nil {
					used = v
				} else {
					ok = false
				}
			}
		}
		if !ok {
			strip(e, RuleMaxPositionNotional, "position exposure for "+string(uid)+" could not be valued; stripped conservatively", adjustments)
			continue
		}

		budget, err := maxPositionNotional.Sub(used)
		if err != nil || budget.Sign() <= 0 {
			strip(e, RuleMaxPositionNotional, "position notional limit already reached for "+string(uid), adjustments)
			continue
		}

		cost, costOK := notionalOf(e.dec.Quantity, instr.Lot, price)
		if costOK && cost.Cmp(budget) <= 0 {
			committed[uid] = addOrZero(committed[uid], cost)
			continue
		}

		k := maxWithinBudget(e.dec.Quantity, func(kk int64) bool {
			n, ok := notionalOf(kk, instr.Lot, price)
			return ok && n.Cmp(budget) <= 0
		})
		if k <= 0 {
			strip(e, RuleMaxPositionNotional, "less than one lot fits within the position notional limit for "+string(uid), adjustments)
			continue
		}
		shrink(e, k, RuleMaxPositionNotional, "shrunk to fit the position notional limit for "+string(uid), adjustments)
		if n, ok := notionalOf(k, instr.Lot, price); ok {
			committed[uid] = addOrZero(committed[uid], n)
		}
	}
}

// applyOversell prevents selling more than is held net of commitments (Phase 1
// forbids shorting). Walking live sells in action order, each sell's sellable
// quantity is the position minus lots already resting to sell (OpenIntents.
// SellLots) minus sells already committed earlier in this same cycle for the
// instrument. A sell beyond that is shrunk to fit; a sell with nothing left to
// sell is stripped. Close actions are not sized here — they carry no quantity
// and are resolved to a concrete sell upstream (DESIGN §3) — so only ActionSell
// is netted.
func applyOversell(entries []*entry, state State, adjustments *[]Adjustment) {
	committed := make(map[model.InstrumentUID]int64)
	for _, e := range entries {
		if !e.live || e.dec.Action != model.ActionSell {
			continue
		}
		uid := e.dec.InstrumentUID
		available := state.Positions[uid].QtyLots - state.OpenIntents[uid].SellLots - committed[uid]
		if available <= 0 {
			strip(e, RuleOversell, "no lots available to sell for "+string(uid)+" net of pending sells", adjustments)
			continue
		}
		if e.dec.Quantity > available {
			shrink(e, available, RuleOversell, "shrunk to the position net of pending sells for "+string(uid), adjustments)
			committed[uid] += available
			continue
		}
		committed[uid] += e.dec.Quantity
	}
}

// existingExposure returns the combined notional of an instrument's
// existing position and pending buy intents, or ok=false if either has a
// non-zero quantity that cannot be priced.
func existingExposure(uid model.InstrumentUID, lot int64, state State) (model.Decimal, bool) {
	pos := state.Positions[uid]
	pendBuyLots := state.OpenIntents[uid].buyLots()
	if pos.QtyLots == 0 && pendBuyLots == 0 {
		return model.Decimal{}, true
	}
	price, hasPrice := exposurePrice(uid, state)
	if !hasPrice {
		return model.Decimal{}, false
	}
	total := model.Decimal{}
	if pos.QtyLots != 0 {
		n, ok := notionalOf(pos.QtyLots, lot, price)
		if !ok {
			return model.Decimal{}, false
		}
		v, err := total.Add(n)
		if err != nil {
			return model.Decimal{}, false
		}
		total = v
	}
	if pendBuyLots != 0 {
		n, ok := notionalOf(pendBuyLots, lot, price)
		if !ok {
			return model.Decimal{}, false
		}
		v, err := total.Add(n)
		if err != nil {
			return model.Decimal{}, false
		}
		total = v
	}
	return total, true
}

// addOrZero adds n to a possibly-absent running total (map lookups on
// model.Decimal already zero-value correctly; this only exists to keep call
// sites free of an inline error check for an addition that, by
// construction, only overflows if maxPositionNotional itself would have
// already, which was already handled).
func addOrZero(sum, n model.Decimal) model.Decimal {
	if v, err := sum.Add(n); err == nil {
		return v
	}
	return sum
}

// applyTotalExposure is rule 6. It sums the notional of every existing
// position and every pending buy intent across the whole portfolio — not
// just instruments with a decision this cycle — to form the used baseline,
// then walks live buys in action order, shrinking or stripping (whole lots,
// floor) to keep the running total within maxTotalExposure. If any leg of
// the baseline cannot be priced, the remaining budget is treated as zero
// (every buy strips) rather than silently understating exposure: DESIGN.md
// §8 rules out ever inferring headroom that cannot be proven.
func applyTotalExposure(entries []*entry, state State, maxTotalExposure model.Decimal, adjustments *[]Adjustment) {
	baseline := model.Decimal{}
	incomplete := false
	sumLeg := func(uid model.InstrumentUID, qtyLots int64) {
		if qtyLots == 0 {
			return
		}
		instr, hasInstr := state.Instruments[uid]
		if !hasInstr {
			incomplete = true
			return
		}
		price, hasPrice := exposurePrice(uid, state)
		if !hasPrice {
			incomplete = true
			return
		}
		n, ok := notionalOf(qtyLots, instr.Lot, price)
		if !ok {
			incomplete = true
			return
		}
		v, err := baseline.Add(n)
		if err != nil {
			incomplete = true
			return
		}
		baseline = v
	}
	for uid, pos := range state.Positions {
		sumLeg(uid, pos.QtyLots)
	}
	for uid, pend := range state.OpenIntents {
		sumLeg(uid, pend.buyLots())
	}

	budget, err := maxTotalExposure.Sub(baseline)
	if err != nil || incomplete {
		budget = model.Decimal{}
	}

	for _, e := range entries {
		if !e.live || e.dec.Action != model.ActionBuy {
			continue
		}
		uid := e.dec.InstrumentUID
		instr, hasInstr := state.Instruments[uid]
		if !hasInstr {
			strip(e, RuleMaxTotalExposure, "no instrument metadata for "+string(uid), adjustments)
			continue
		}
		price, hasPrice := priceForBuy(e.dec, state.Quotes[uid])
		if !hasPrice {
			strip(e, RuleMaxTotalExposure, "no price available to value buy for "+string(uid), adjustments)
			continue
		}

		cost, costOK := notionalOf(e.dec.Quantity, instr.Lot, price)
		if costOK && budget.Sign() > 0 && cost.Cmp(budget) <= 0 {
			if v, err := budget.Sub(cost); err == nil {
				budget = v
				continue
			}
		}

		k := maxWithinBudget(e.dec.Quantity, func(kk int64) bool {
			n, ok := notionalOf(kk, instr.Lot, price)
			return ok && n.Cmp(budget) <= 0
		})
		if k <= 0 {
			strip(e, RuleMaxTotalExposure, "total exposure limit leaves no room for "+string(uid), adjustments)
			continue
		}
		shrink(e, k, RuleMaxTotalExposure, "shrunk to fit the total exposure limit for "+string(uid), adjustments)
		if n, ok := notionalOf(k, instr.Lot, price); ok {
			if v, err := budget.Sub(n); err == nil {
				budget = v
			}
		}
	}
}

// applyCashFloor is rule 7. It walks live buys in action order, estimating
// each one's cash cost — qty*price with a slippage buffer for market orders
// (limit orders cost their limit price, uncushioned) plus a fee buffer —
// and shrinks or strips (whole lots, floor) so cash minus the running
// committed cost never drops below cashFloor.
//
// Cash already committed to resting buy intents from earlier cycles is
// reserved up front: their conservative cost is subtracted from the budget
// before this cycle's buys are sized, so a new buy can never spend cash a
// pending buy is already going to consume and drive the account below the
// floor once both settle. If any pending buy cannot be valued, the whole
// budget is forced to zero (fail closed) rather than understating the
// commitment, mirroring rule 6's treatment of an unpriceable baseline.
func applyCashFloor(entries []*entry, state State, cashFloor model.Decimal, adjustments *[]Adjustment) {
	budget, err := state.Cash.Sub(cashFloor)
	if err != nil {
		budget = model.Decimal{}
	}
	if reserved, ok := reservedPendingBuyCost(state); !ok {
		budget = model.Decimal{}
	} else if v, err := budget.Sub(reserved); err == nil {
		budget = v
	} else {
		budget = model.Decimal{}
	}

	for _, e := range entries {
		if !e.live || e.dec.Action != model.ActionBuy {
			continue
		}
		uid := e.dec.InstrumentUID
		instr, hasInstr := state.Instruments[uid]
		if !hasInstr {
			strip(e, RuleCashFloor, "no instrument metadata for "+string(uid), adjustments)
			continue
		}
		price, hasPrice := priceForBuy(e.dec, state.Quotes[uid])
		if !hasPrice {
			strip(e, RuleCashFloor, "no price available to estimate cost for "+string(uid), adjustments)
			continue
		}
		effectivePrice := price
		if e.dec.OrderType == model.OrderMarket && state.SlippageBufferBps != 0 {
			if p, err := price.MulBps(10000 + state.SlippageBufferBps); err == nil {
				effectivePrice = p
			}
		}
		costWithFee := func(kk int64) (model.Decimal, bool) {
			n, ok := notionalOf(kk, instr.Lot, effectivePrice)
			if !ok {
				return model.Decimal{}, false
			}
			fee, err := n.MulBps(state.FeeBufferBps)
			if err != nil {
				return model.Decimal{}, false
			}
			total, err := n.Add(fee)
			if err != nil {
				return model.Decimal{}, false
			}
			return total, true
		}

		cost, costOK := costWithFee(e.dec.Quantity)
		if costOK && cost.Cmp(budget) <= 0 {
			if v, err := budget.Sub(cost); err == nil {
				budget = v
				continue
			}
		}

		k := maxWithinBudget(e.dec.Quantity, func(kk int64) bool {
			c, ok := costWithFee(kk)
			return ok && c.Cmp(budget) <= 0
		})
		if k <= 0 {
			strip(e, RuleCashFloor, "cash floor leaves no room for "+string(uid), adjustments)
			continue
		}
		shrink(e, k, RuleCashFloor, "shrunk to preserve the cash floor for "+string(uid), adjustments)
		if c, ok := costWithFee(k); ok {
			if v, err := budget.Sub(c); err == nil {
				budget = v
			}
		}
	}
}

// reservedPendingBuyCost is the conservative cash the account has already
// committed to resting buy intents across every instrument. Each pending buy is
// priced at the most cash it can consume: a limit buy at its resting limit
// price (the ceiling a limit fill can reach — the current mark would understate
// it whenever the limit sits above the mark, letting a second buy pass and both
// fills breach the floor), a market buy at the current mark padded for
// slippage. A fee buffer is added in both cases. ok is false if any pending buy
// cannot be valued, so the caller can force the cash budget to zero rather than
// let an unpriceable commitment silently drop out of the floor.
func reservedPendingBuyCost(state State) (model.Decimal, bool) {
	total := model.Decimal{}
	for uid, pend := range state.OpenIntents {
		instr, hasInstr := state.Instruments[uid]
		for _, b := range pend.Buys {
			if b.Lots <= 0 {
				continue
			}
			if !hasInstr {
				return model.Decimal{}, false
			}
			price, ok := pendingBuyPrice(uid, b, state)
			if !ok {
				return model.Decimal{}, false
			}
			n, ok := notionalOf(b.Lots, instr.Lot, price)
			if !ok {
				return model.Decimal{}, false
			}
			if state.FeeBufferBps != 0 {
				fee, err := n.MulBps(state.FeeBufferBps)
				if err != nil {
					return model.Decimal{}, false
				}
				n, err = n.Add(fee)
				if err != nil {
					return model.Decimal{}, false
				}
			}
			v, err := total.Add(n)
			if err != nil {
				return model.Decimal{}, false
			}
			total = v
		}
	}
	return total, true
}

// pendingBuyPrice returns the per-share price a resting buy can consume cash up
// to: a valid limit order's limit price, else the instrument's current mark
// padded by the market-slippage buffer. ok is false when a market pending buy
// has no mark to price against.
func pendingBuyPrice(uid model.InstrumentUID, b PendingBuy, state State) (model.Decimal, bool) {
	if b.OrderType == model.OrderLimit && b.LimitPrice.Sign() > 0 {
		return b.LimitPrice, true
	}
	mark, ok := exposurePrice(uid, state)
	if !ok {
		return model.Decimal{}, false
	}
	if state.SlippageBufferBps != 0 {
		p, err := mark.MulBps(10000 + state.SlippageBufferBps)
		if err != nil {
			return model.Decimal{}, false
		}
		return p, true
	}
	return mark, true
}
