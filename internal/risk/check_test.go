package risk

import (
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/model"
)

const (
	uidA model.InstrumentUID = "uid-a"
	uidB model.InstrumentUID = "uid-b"
	uidC model.InstrumentUID = "uid-c"
)

// --- test fixtures -----------------------------------------------------

func instrument(uid model.InstrumentUID, ticker string, lot int64) model.Instrument {
	return model.Instrument{
		InstrumentRef:     model.InstrumentRef{UID: uid, Ticker: ticker},
		Lot:               lot,
		MinPriceIncrement: model.MustDecimal("0.01"),
		Currency:          "RUB",
	}
}

func quote(last string) model.Quote {
	return model.Quote{Last: model.MustDecimal(last), Ask: model.MustDecimal(last), Bid: model.MustDecimal(last)}
}

func buy(uid model.InstrumentUID, qty int64) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionBuy, Quantity: qty, OrderType: model.OrderMarket, TimeInForce: model.TIFDay}
}

func limitBuy(uid model.InstrumentUID, qty int64, limitPrice string) model.Decision {
	lp := model.MustDecimal(limitPrice)
	return model.Decision{InstrumentUID: uid, Action: model.ActionBuy, Quantity: qty, OrderType: model.OrderLimit, LimitPrice: &lp, TimeInForce: model.TIFDay}
}

func sell(uid model.InstrumentUID, qty int64) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionSell, Quantity: qty, OrderType: model.OrderMarket, TimeInForce: model.TIFDay}
}

func closeAction(uid model.InstrumentUID, qty int64) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionClose, Quantity: qty, OrderType: model.OrderMarket, TimeInForce: model.TIFDay}
}

func hold(uid model.InstrumentUID) model.Decision {
	return model.Decision{InstrumentUID: uid, Action: model.ActionHold, TimeInForce: model.TIFDay}
}

// wideOpenLimits has no binding constraints, so a test can override just
// the one limit it exercises.
func wideOpenLimits() config.RiskConfig {
	return config.RiskConfig{
		MaxPositionNotional: "1000000",
		MaxTotalExposure:    "1000000",
		MaxOrdersPerCycle:   100,
		MaxOrdersPerDay:     100,
		MaxDailyLoss:        "1000000",
		CashFloor:           "0",
	}
}

// wideOpenState has instruments A/B/C at lot 1, quoted at 100/50/10, ample
// cash, no positions, and no pending intents, so a test can override just
// what it needs.
func wideOpenState() State {
	return State{
		Cash: model.MustDecimal("1000000"),
		Instruments: map[model.InstrumentUID]model.Instrument{
			uidA: instrument(uidA, "AAA", 1),
			uidB: instrument(uidB, "BBB", 1),
			uidC: instrument(uidC, "CCC", 1),
		},
		Quotes: map[model.InstrumentUID]model.Quote{
			uidA: quote("100"),
			uidB: quote("50"),
			uidC: quote("10"),
		},
		Positions:   map[model.InstrumentUID]Position{},
		OpenIntents: map[model.InstrumentUID]PendingIntents{},
	}
}

func allowedQty(t *testing.T, allowed []model.Decision, uid model.InstrumentUID) (int64, bool) {
	t.Helper()
	for _, d := range allowed {
		if d.InstrumentUID == uid {
			return d.Quantity, true
		}
	}
	return 0, false
}

func adjustmentFor(res Result, index int) []Adjustment {
	var out []Adjustment
	for _, a := range res.Adjustments {
		if a.Index == index {
			out = append(out, a)
		}
	}
	return out
}

// --- pass-through --------------------------------------------------------

func TestCheck_PassThroughWhenNothingBinds(t *testing.T) {
	state := wideOpenState()
	state.Positions[uidB] = Position{QtyLots: 100, LastPrice: model.MustDecimal("50")} // backs the sell
	actions := []model.Decision{buy(uidA, 5), sell(uidB, 3), hold(uidC), closeAction(uidB, 2)}
	res := Check(actions, state, wideOpenLimits())

	if res.Halted {
		t.Fatalf("Halted = true, want false")
	}
	if len(res.Adjustments) != 0 {
		t.Fatalf("Adjustments = %+v, want none", res.Adjustments)
	}
	if !reflect.DeepEqual(res.Allowed, actions) {
		t.Fatalf("Allowed = %+v, want unchanged %+v", res.Allowed, actions)
	}
}

func TestCheck_EmptyActions(t *testing.T) {
	res := Check(nil, wideOpenState(), wideOpenLimits())
	if res.Halted {
		t.Fatalf("Halted = true, want false")
	}
	if len(res.Allowed) != 0 || len(res.Adjustments) != 0 {
		t.Fatalf("Result = %+v, want empty", res)
	}
}

func TestCheck_ZeroValueConfigAndState(t *testing.T) {
	// An unconfigured RiskConfig has empty limit strings, which
	// parseLimitOrZero treats as "0". A max_daily_loss of 0 has zero loss
	// tolerance, so the kill switch engages even with a zero DayPnL (0 >=
	// 0) — see applyKillSwitch's doc comment. This is deliberate,
	// fail-closed behavior for an unconfigured/zero-value limit, not a bug.
	res := Check(nil, State{}, config.RiskConfig{})
	if !res.Halted {
		t.Fatalf("Halted = false, want true for zero-value config (0 loss tolerance)")
	}
	if len(res.Allowed) != 0 {
		t.Fatalf("Allowed = %+v, want empty", res.Allowed)
	}
}

// --- rule 1: kill switch -------------------------------------------------

func TestKillSwitch(t *testing.T) {
	t.Run("engages at the loss boundary and flattens", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxDailyLoss = "5000"
		state := wideOpenState()
		state.DayPnL = model.MustDecimal("-5000")                                          // exactly at the limit: >=
		state.Positions[uidB] = Position{QtyLots: 100, LastPrice: model.MustDecimal("50")} // backs the sell exit

		actions := []model.Decision{buy(uidA, 1), sell(uidB, 1), hold(uidC), closeAction(uidB, 1)}
		res := Check(actions, state, limits)

		if !res.Halted {
			t.Fatalf("Halted = false, want true")
		}
		if _, ok := allowedQty(t, res.Allowed, uidA); ok {
			t.Errorf("buy uidA should be stripped")
		}
		if _, ok := allowedQty(t, res.Allowed, uidC); ok {
			t.Errorf("hold uidC should be stripped in flatten-only mode")
		}
		if q, ok := allowedQty(t, res.Allowed, uidB); !ok {
			t.Errorf("sell/close uidB should pass")
		} else if q != 1 {
			t.Errorf("sell qty = %d, want 1 (unmodified)", q)
		}
		for _, idx := range []int{0, 2} { // buy, hold
			adjs := adjustmentFor(res, idx)
			if len(adjs) != 1 || adjs[0].Rule != RuleDailyLossKillSwitch || adjs[0].Adjusted != nil {
				t.Errorf("index %d adjustments = %+v, want one strip tagged daily_loss_kill_switch", idx, adjs)
			}
		}
	})

	t.Run("does not engage just under the boundary", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxDailyLoss = "5000"
		state := wideOpenState()
		state.DayPnL = model.MustDecimal("-4999.999999999")

		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)
		if res.Halted {
			t.Fatalf("Halted = true, want false")
		}
		if _, ok := allowedQty(t, res.Allowed, uidA); !ok {
			t.Errorf("buy should pass when under the loss limit")
		}
	})

	t.Run("a gain never engages the switch", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxDailyLoss = "5000"
		state := wideOpenState()
		state.DayPnL = model.MustDecimal("50000")

		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)
		if res.Halted {
			t.Fatalf("Halted = true, want false for a positive DayPnL")
		}
	})
}

// --- rule 2: allowlist ----------------------------------------------------

func TestAllowlist(t *testing.T) {
	t.Run("empty allowlist restricts nothing", func(t *testing.T) {
		res := Check([]model.Decision{buy(uidA, 1), buy(uidB, 1)}, wideOpenState(), wideOpenLimits())
		if len(res.Adjustments) != 0 {
			t.Fatalf("Adjustments = %+v, want none", res.Adjustments)
		}
	})

	t.Run("matches by UID", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.Allowlist = []string{string(uidA)}
		res := Check([]model.Decision{buy(uidA, 1), buy(uidB, 1)}, wideOpenState(), limits)

		if _, ok := allowedQty(t, res.Allowed, uidA); !ok {
			t.Errorf("uidA should pass (allowlisted by UID)")
		}
		if _, ok := allowedQty(t, res.Allowed, uidB); ok {
			t.Errorf("uidB should be stripped (not allowlisted)")
		}
	})

	t.Run("matches by ticker", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.Allowlist = []string{"BBB"} // uidB's ticker in wideOpenState
		res := Check([]model.Decision{buy(uidA, 1), buy(uidB, 1)}, wideOpenState(), limits)

		if _, ok := allowedQty(t, res.Allowed, uidB); !ok {
			t.Errorf("uidB should pass (allowlisted by ticker)")
		}
		if _, ok := allowedQty(t, res.Allowed, uidA); ok {
			t.Errorf("uidA should be stripped (not allowlisted)")
		}
	})

	t.Run("sell and close always pass regardless of allowlist", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.Allowlist = []string{string(uidC)} // neither A nor B
		state := wideOpenState()
		state.Positions[uidA] = Position{QtyLots: 100, LastPrice: model.MustDecimal("100")} // backs the sell exit
		actions := []model.Decision{sell(uidA, 1), closeAction(uidB, 1)}
		res := Check(actions, state, limits)

		if len(res.Adjustments) != 0 {
			t.Fatalf("Adjustments = %+v, want none (exits always pass)", res.Adjustments)
		}
		if len(res.Allowed) != 2 {
			t.Fatalf("Allowed = %+v, want both exits kept", res.Allowed)
		}
	})
}

// --- rules 3-4: order caps -------------------------------------------------

func TestOrderCaps(t *testing.T) {
	t.Run("per-cycle cap keeps the first N order-producing actions, hold uncounted", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxOrdersPerCycle = 2
		state := wideOpenState()
		state.Positions[uidB] = Position{QtyLots: 100, LastPrice: model.MustDecimal("50")} // backs the sell
		actions := []model.Decision{buy(uidA, 1), hold(uidA), sell(uidB, 1), buy(uidC, 1)}
		res := Check(actions, state, limits)

		if _, ok := allowedQty(t, res.Allowed, uidA); !ok {
			t.Errorf("first order (buy uidA) should be kept")
		}
		if _, ok := allowedQty(t, res.Allowed, uidB); !ok {
			t.Errorf("second order (sell uidB) should be kept")
		}
		if _, ok := allowedQty(t, res.Allowed, uidC); ok {
			t.Errorf("third order (buy uidC) should be stripped by the per-cycle cap")
		}
		if len(res.Allowed) != 3 { // buy A, hold A, sell B (hold always passes)
			t.Fatalf("Allowed = %+v, want hold to survive uncounted", res.Allowed)
		}
		adjs := adjustmentFor(res, 3)
		if len(adjs) != 1 || adjs[0].Rule != RuleMaxOrdersPerCycle {
			t.Errorf("index 3 adjustments = %+v, want one strip tagged max_orders_per_cycle", adjs)
		}
	})

	t.Run("daily cap accounts for OrdersToday", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxOrdersPerDay = 20
		state := wideOpenState()
		state.OrdersToday = 18
		actions := []model.Decision{buy(uidA, 1), buy(uidB, 1), buy(uidC, 1)}
		res := Check(actions, state, limits)

		if len(res.Allowed) != 2 {
			t.Fatalf("Allowed = %+v, want 2 kept (20-18=2 remaining budget)", res.Allowed)
		}
		if _, ok := allowedQty(t, res.Allowed, uidC); ok {
			t.Errorf("third action should be stripped by the daily cap")
		}
		adjs := adjustmentFor(res, 2)
		if len(adjs) != 1 || adjs[0].Rule != RuleMaxOrdersPerDay {
			t.Errorf("index 2 adjustments = %+v, want one strip tagged max_orders_per_day", adjs)
		}
	})

	t.Run("daily budget already exhausted strips everything", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxOrdersPerDay = 20
		state := wideOpenState()
		state.OrdersToday = 25 // already over
		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty", res.Allowed)
		}
	})
}

// --- rule 5: per-instrument position notional -----------------------------

func TestPositionNotional(t *testing.T) {
	t.Run("passes exactly at the boundary", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxPositionNotional = "1000"
		state := wideOpenState()                                     // uidA @ 100/lot
		res := Check([]model.Decision{buy(uidA, 10)}, state, limits) // 10*100=1000

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 10 {
			t.Fatalf("got qty=%d ok=%v, want 10 unmodified (exactly at limit)", q, ok)
		}
		if len(res.Adjustments) != 0 {
			t.Errorf("Adjustments = %+v, want none at the exact boundary", res.Adjustments)
		}
	})

	t.Run("shrinks to the floor lot when it overshoots", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxPositionNotional = "1000"
		state := wideOpenState()
		state.Positions[uidA] = Position{QtyLots: 5, LastPrice: model.MustDecimal("100")} // 500 existing
		res := Check([]model.Decision{buy(uidA, 6)}, state, limits)                       // 500+600=1100 > 1000

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 5 { // budget 500 / 100 per lot = 5 lots
			t.Fatalf("got qty=%d ok=%v, want shrunk to 5", q, ok)
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) != 1 || adjs[0].Rule != RuleMaxPositionNotional || adjs[0].Adjusted == nil || adjs[0].Adjusted.Quantity != 5 {
			t.Fatalf("adjustment = %+v, want a shrink to 5 tagged max_position_notional", adjs)
		}
	})

	t.Run("strips entirely when the limit is already reached", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxPositionNotional = "1000"
		state := wideOpenState()
		state.Positions[uidA] = Position{QtyLots: 10, LastPrice: model.MustDecimal("100")} // already at 1000
		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if _, ok := allowedQty(t, res.Allowed, uidA); ok {
			t.Fatalf("buy should be stripped, limit already reached")
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) != 1 || adjs[0].Rule != RuleMaxPositionNotional || adjs[0].Adjusted != nil {
			t.Fatalf("adjustment = %+v, want a strip tagged max_position_notional", adjs)
		}
	})

	t.Run("counts pending buy intents against the same limit", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxPositionNotional = "1000"
		state := wideOpenState()
		state.OpenIntents[uidA] = PendingIntents{BuyLots: 5} // 500 pending @ last price 100
		res := Check([]model.Decision{buy(uidA, 6)}, state, limits)

		q, _ := allowedQty(t, res.Allowed, uidA)
		if q != 5 {
			t.Fatalf("qty = %d, want shrunk to 5 (500 pending + 500 room)", q)
		}
	})

	t.Run("accumulates across multiple buys for the same instrument in one cycle", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxPositionNotional = "1000"
		res := Check([]model.Decision{buy(uidA, 6), buy(uidA, 6)}, wideOpenState(), limits)

		q0, _ := allowedQty2(res.Allowed, 0)
		q1, ok1 := allowedQty2(res.Allowed, 1)
		if q0 != 6 {
			t.Fatalf("first buy qty = %d, want 6 (fits standalone: 600<=1000)", q0)
		}
		if !ok1 || q1 != 4 {
			t.Fatalf("second buy qty=%d ok=%v, want shrunk to 4 (400 room left after the first)", q1, ok1)
		}
	})

	t.Run("strips when instrument metadata is missing", func(t *testing.T) {
		limits := wideOpenLimits()
		state := wideOpenState()
		unknown := model.InstrumentUID("ghost")
		res := Check([]model.Decision{buy(unknown, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty for an unknown instrument", res.Allowed)
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) != 1 || adjs[0].Rule != RuleMaxPositionNotional {
			t.Fatalf("adjustment = %+v, want a strip tagged max_position_notional", adjs)
		}
	})

	t.Run("strips a market buy with no quote at all", func(t *testing.T) {
		limits := wideOpenLimits()
		state := wideOpenState()
		delete(state.Quotes, uidA)
		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty when no price is available", res.Allowed)
		}
	})

	t.Run("strips rather than wraps when the existing position overflows int64 shares", func(t *testing.T) {
		// QtyLots * Lot overflows int64 (well past MaxInt64), which a raw
		// multiply would silently wrap instead of erroring. If that wrapped
		// value were trusted, it could present as a small or negative
		// notional and let the buy sail through a limit it should never
		// have passed.
		limits := wideOpenLimits()
		state := wideOpenState()
		state.Positions[uidA] = Position{QtyLots: math.MaxInt64 / 2, LastPrice: model.MustDecimal("100")}
		state.Instruments[uidA] = instrument(uidA, "AAA", 4) // *4 overflows int64

		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty: the existing position can't be valued without overflowing", res.Allowed)
		}
	})

	t.Run("strips on a negative quote price instead of treating it as usable", func(t *testing.T) {
		// A negative price is not "missing" (an IsZero-only guard would
		// have let it through); it must be rejected outright, since a
		// negative notional would net down a budget instead of consuming
		// it -- silently widening what the limit permits.
		limits := wideOpenLimits()
		state := wideOpenState()
		state.Quotes[uidA] = model.Quote{Ask: model.MustDecimal("-100"), Last: model.MustDecimal("-100")}

		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty for a negative quote price", res.Allowed)
		}
	})

	t.Run("strips on a non-positive instrument lot size", func(t *testing.T) {
		limits := wideOpenLimits()
		state := wideOpenState()
		state.Instruments[uidA] = instrument(uidA, "AAA", 0) // corrupt/unset lot size

		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty for a non-positive lot size", res.Allowed)
		}
	})

	t.Run("a non-positive limit price is not rescued by the market price", func(t *testing.T) {
		// uidA has a perfectly good market quote in wideOpenState; a limit
		// order with an invalid limit price must still be stripped, not
		// silently repriced at the market.
		limits := wideOpenLimits()
		res := Check([]model.Decision{limitBuy(uidA, 1, "0")}, wideOpenState(), limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty: a zero limit price must not fall back to the market quote", res.Allowed)
		}
	})
}

// allowedQty2 returns the decision at position i of allowed (helper for
// tests asserting on order rather than instrument identity, since some
// cases place two decisions for the same UID).
func allowedQty2(allowed []model.Decision, i int) (int64, bool) {
	if i < 0 || i >= len(allowed) {
		return 0, false
	}
	return allowed[i].Quantity, true
}

// --- rule 6: total exposure -------------------------------------------------

func TestTotalExposure(t *testing.T) {
	t.Run("counts existing positions in instruments outside this cycle's actions", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxTotalExposure = "3000"
		state := wideOpenState()
		state.Positions[uidB] = Position{QtyLots: 50, LastPrice: model.MustDecimal("50")} // 2500 baseline, no decision on B this cycle
		res := Check([]model.Decision{buy(uidA, 10)}, state, limits)                      // 10*100=1000, but only 500 room

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 5 {
			t.Fatalf("got qty=%d ok=%v, want shrunk to 5 (500 room / 100 per lot)", q, ok)
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) != 1 || adjs[0].Rule != RuleMaxTotalExposure {
			t.Fatalf("adjustment = %+v, want a shrink tagged max_total_exposure", adjs)
		}
	})

	t.Run("shrinks/strips buys greedily in action order across instruments", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxTotalExposure = "1000"
		// uidA @ 100/lot, uidB @ 50/lot in wideOpenState.
		actions := []model.Decision{buy(uidA, 8), buy(uidB, 10)} // 800 + 500 = 1300 > 1000
		res := Check(actions, wideOpenState(), limits)

		qA, _ := allowedQty(t, res.Allowed, uidA)
		if qA != 8 {
			t.Fatalf("first buy (uidA) qty = %d, want kept at 8 (800<=1000)", qA)
		}
		qB, okB := allowedQty(t, res.Allowed, uidB)
		if !okB || qB != 4 { // 200 room left / 50 per lot = 4
			t.Fatalf("second buy (uidB) qty=%d ok=%v, want shrunk to 4", qB, okB)
		}
	})

	t.Run("unpriceable baseline leg forces zero budget (fail closed)", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.MaxTotalExposure = "1000000"
		state := wideOpenState()
		// A position with quantity but no price anywhere: no Position mark
		// and no quote.
		state.Positions[uidC] = Position{QtyLots: 10}
		delete(state.Quotes, uidC)
		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty: baseline can't be proven safe", res.Allowed)
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) != 1 || adjs[0].Rule != RuleMaxTotalExposure {
			t.Fatalf("adjustment = %+v, want a strip tagged max_total_exposure", adjs)
		}
	})
}

// --- rule 7: cash floor -----------------------------------------------------

func TestCashFloor(t *testing.T) {
	t.Run("passes exactly at the boundary", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "200"
		state := wideOpenState()
		state.Cash = model.MustDecimal("1000")                      // budget 800
		res := Check([]model.Decision{buy(uidA, 8)}, state, limits) // 8*100=800

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 8 {
			t.Fatalf("got qty=%d ok=%v, want 8 unmodified at the exact boundary", q, ok)
		}
		if len(res.Adjustments) != 0 {
			t.Errorf("Adjustments = %+v, want none", res.Adjustments)
		}
	})

	t.Run("shrinks to the floor lot when cash is short", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "200"
		state := wideOpenState()
		state.Cash = model.MustDecimal("1000")                      // budget 800
		res := Check([]model.Decision{buy(uidA, 9)}, state, limits) // 900 > 800

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 8 {
			t.Fatalf("got qty=%d ok=%v, want shrunk to 8", q, ok)
		}
	})

	t.Run("cash already below the floor strips all buys", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "200"
		state := wideOpenState()
		state.Cash = model.MustDecimal("100") // already below floor
		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if len(res.Allowed) != 0 {
			t.Fatalf("Allowed = %+v, want empty", res.Allowed)
		}
	})

	t.Run("slippage buffer pads a market buy's estimated cost", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "200"
		state := wideOpenState()
		state.Cash = model.MustDecimal("1000")                      // budget 800
		state.SlippageBufferBps = 100                               // +1%: effective price 101
		res := Check([]model.Decision{buy(uidA, 8)}, state, limits) // 8*101=808 > 800

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 7 { // 7*101=707<=800, 8*101=808>800
			t.Fatalf("got qty=%d ok=%v, want shrunk to 7 under the slippage buffer", q, ok)
		}
	})

	t.Run("slippage buffer does not apply to limit orders", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "0"
		state := wideOpenState()
		state.Cash = model.MustDecimal("800")
		state.SlippageBufferBps = 100
		res := Check([]model.Decision{limitBuy(uidA, 8, "100")}, state, limits) // 8*100=800, no buffer

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 8 {
			t.Fatalf("got qty=%d ok=%v, want 8 unmodified (limit price ignores the slippage buffer)", q, ok)
		}
	})

	t.Run("fee buffer reserves a fraction of notional", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "0"
		state := wideOpenState()
		state.Cash = model.MustDecimal("808")
		state.FeeBufferBps = 100                                    // 1% fee reserve
		res := Check([]model.Decision{buy(uidA, 8)}, state, limits) // 800 notional + 8 fee = 808

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 8 {
			t.Fatalf("got qty=%d ok=%v, want 8 (exactly covers notional+fee)", q, ok)
		}
	})

	t.Run("commits cash across multiple buys in action order", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "0"
		state := wideOpenState()
		state.Cash = model.MustDecimal("1000")
		actions := []model.Decision{buy(uidA, 8), buy(uidB, 10)} // 800 + 500 = 1300 > 1000
		res := Check(actions, state, limits)

		qA, _ := allowedQty(t, res.Allowed, uidA)
		if qA != 8 {
			t.Fatalf("first buy qty = %d, want kept at 8", qA)
		}
		qB, okB := allowedQty(t, res.Allowed, uidB)
		if !okB || qB != 4 { // 200 cash left / 50 per lot = 4
			t.Fatalf("second buy qty=%d ok=%v, want shrunk to 4", qB, okB)
		}
	})
}

// --- overflow fail-closed on the candidate buy side --------------------------

func TestCheck_OverflowingCandidateQuantityDoesNotBypassNotional(t *testing.T) {
	// The candidate buy itself (not an existing position) carries a lot count
	// whose lots*lot product wraps int64 to zero (2^62 * 4 == 2^64). The old
	// raw multiply booked that as a zero notional, so the buy sailed past
	// every monetary limit unchanged.
	limits := wideOpenLimits()
	limits.MaxPositionNotional = "1000"
	state := wideOpenState()
	state.Instruments[uidA] = instrument(uidA, "AAA", 4)
	const wrapping int64 = 1 << 62
	res := Check([]model.Decision{buy(uidA, wrapping)}, state, limits)

	q, ok := allowedQty(t, res.Allowed, uidA)
	if ok && q == wrapping {
		t.Fatalf("overflowing buy passed unchanged at qty %d: notional check was bypassed", q)
	}
	// Anything that survives must genuinely fit: price 100, lot 4 => at most 2
	// lots (800) fit within a 1000 notional; 3 lots (1200) do not.
	if ok && q > 2 {
		t.Fatalf("allowed qty %d exceeds what fits the notional limit; want <= 2 or stripped", q)
	}
}

// --- rule 7: cash reserved for pending buys ---------------------------------

func TestCashFloor_ReservesPendingBuys(t *testing.T) {
	t.Run("pending buy intents reserve cash before this cycle's buys", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "0"
		state := wideOpenState()
		state.Cash = model.MustDecimal("1000")
		// 5 lots of uidA already resting to buy @ 100 => 500 reserved, 500 free.
		state.OpenIntents[uidA] = PendingIntents{BuyLots: 5}
		res := Check([]model.Decision{buy(uidA, 8)}, state, limits) // wants 800

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 5 { // 500 free / 100 per lot
			t.Fatalf("got qty=%d ok=%v, want shrunk to 5 (pending buys reserve 500)", q, ok)
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) == 0 || adjs[len(adjs)-1].Rule != RuleCashFloor {
			t.Fatalf("adjustments = %+v, want the final shrink tagged cash_floor", adjs)
		}
	})

	t.Run("pending buys can consume the whole floor and strip a new buy", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "0"
		state := wideOpenState()
		state.Cash = model.MustDecimal("1000")
		state.OpenIntents[uidA] = PendingIntents{BuyLots: 10} // 1000 reserved, nothing free
		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if _, ok := allowedQty(t, res.Allowed, uidA); ok {
			t.Fatalf("buy should be stripped: pending buys already reserve the whole budget")
		}
	})

	t.Run("an unpriceable pending buy forces the budget to zero", func(t *testing.T) {
		limits := wideOpenLimits()
		limits.CashFloor = "0"
		state := wideOpenState()
		state.Cash = model.MustDecimal("1000000")
		// A pending buy on an instrument that cannot be priced (no mark, no quote).
		state.OpenIntents[uidC] = PendingIntents{BuyLots: 5}
		delete(state.Quotes, uidC)
		res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

		if _, ok := allowedQty(t, res.Allowed, uidA); ok {
			t.Fatalf("buy should be stripped: an unpriceable pending commitment forces a zero cash budget")
		}
	})
}

// --- rule 8: oversell -------------------------------------------------------

func TestOversell(t *testing.T) {
	t.Run("nets a sell against the position minus pending sells", func(t *testing.T) {
		state := wideOpenState()
		state.Positions[uidA] = Position{QtyLots: 10, LastPrice: model.MustDecimal("100")}
		state.OpenIntents[uidA] = PendingIntents{SellLots: 8} // 8 already pending
		res := Check([]model.Decision{sell(uidA, 5)}, state, wideOpenLimits())

		q, ok := allowedQty(t, res.Allowed, uidA)
		if !ok || q != 2 { // 10 held - 8 pending = 2 sellable
			t.Fatalf("got qty=%d ok=%v, want shrunk to 2", q, ok)
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) != 1 || adjs[0].Rule != RuleOversell || adjs[0].Adjusted == nil || adjs[0].Adjusted.Quantity != 2 {
			t.Fatalf("adjustment = %+v, want a shrink to 2 tagged oversell", adjs)
		}
	})

	t.Run("strips a sell with nothing left to sell", func(t *testing.T) {
		state := wideOpenState()
		state.Positions[uidA] = Position{QtyLots: 5, LastPrice: model.MustDecimal("100")}
		state.OpenIntents[uidA] = PendingIntents{SellLots: 5} // fully committed already
		res := Check([]model.Decision{sell(uidA, 1)}, state, wideOpenLimits())

		if _, ok := allowedQty(t, res.Allowed, uidA); ok {
			t.Fatalf("sell should be stripped: no lots left to sell net of pending sells")
		}
		adjs := adjustmentFor(res, 0)
		if len(adjs) != 1 || adjs[0].Rule != RuleOversell || adjs[0].Adjusted != nil {
			t.Fatalf("adjustment = %+v, want a strip tagged oversell", adjs)
		}
	})

	t.Run("nets earlier same-cycle sells for the same instrument", func(t *testing.T) {
		state := wideOpenState()
		state.Positions[uidA] = Position{QtyLots: 10, LastPrice: model.MustDecimal("100")}
		res := Check([]model.Decision{sell(uidA, 7), sell(uidA, 7)}, state, wideOpenLimits())

		q0, _ := allowedQty2(res.Allowed, 0)
		q1, ok1 := allowedQty2(res.Allowed, 1)
		if q0 != 7 {
			t.Fatalf("first sell qty = %d, want 7 (fits the 10-lot position)", q0)
		}
		if !ok1 || q1 != 3 { // 10 - 7 already committed = 3 left
			t.Fatalf("second sell qty=%d ok=%v, want shrunk to 3", q1, ok1)
		}
	})

	t.Run("a sell with no position at all is stripped", func(t *testing.T) {
		res := Check([]model.Decision{sell(uidA, 1)}, wideOpenState(), wideOpenLimits())
		if _, ok := allowedQty(t, res.Allowed, uidA); ok {
			t.Fatalf("a sell with no position must be stripped")
		}
	})
}

// --- rule ordering interactions ---------------------------------------------

func TestRuleOrdering_NotionalThenCashFloor(t *testing.T) {
	limits := wideOpenLimits()
	limits.MaxPositionNotional = "1000" // shrinks 20 lots @100 down to 10
	limits.CashFloor = "400"
	state := wideOpenState()
	state.Cash = model.MustDecimal("1000") // budget 600 -> shrinks 10 down to 6

	res := Check([]model.Decision{buy(uidA, 20)}, state, limits)

	q, ok := allowedQty(t, res.Allowed, uidA)
	if !ok || q != 6 {
		t.Fatalf("got qty=%d ok=%v, want 6 after both shrinks compose", q, ok)
	}

	adjs := adjustmentFor(res, 0)
	if len(adjs) != 2 {
		t.Fatalf("Adjustments for index 0 = %+v, want exactly 2 (notional, then cash floor)", adjs)
	}
	if adjs[0].Rule != RuleMaxPositionNotional || adjs[0].Original.Quantity != 20 || adjs[0].Adjusted == nil || adjs[0].Adjusted.Quantity != 10 {
		t.Errorf("first adjustment = %+v, want max_position_notional 20->10", adjs[0])
	}
	if adjs[1].Rule != RuleCashFloor || adjs[1].Original.Quantity != 10 || adjs[1].Adjusted == nil || adjs[1].Adjusted.Quantity != 6 {
		t.Errorf("second adjustment = %+v, want cash_floor 10->6, chained from the first", adjs[1])
	}
}

func TestRuleOrdering_KillSwitchPrecedesAllowlist(t *testing.T) {
	// A non-allowlisted buy under a kill switch is recorded once, by the
	// kill switch (the first rule to touch it) — the allowlist rule never
	// sees it because it is already dead.
	limits := wideOpenLimits()
	limits.MaxDailyLoss = "100"
	limits.Allowlist = []string{string(uidB)} // uidA is not allowlisted
	state := wideOpenState()
	state.DayPnL = model.MustDecimal("-500")

	res := Check([]model.Decision{buy(uidA, 1)}, state, limits)

	adjs := adjustmentFor(res, 0)
	if len(adjs) != 1 || adjs[0].Rule != RuleDailyLossKillSwitch {
		t.Fatalf("adjustments = %+v, want exactly one strip tagged daily_loss_kill_switch", adjs)
	}
}

// --- stable ordering & determinism ------------------------------------------

func TestCheck_StableOrderingOfAllowed(t *testing.T) {
	limits := wideOpenLimits()
	limits.Allowlist = []string{string(uidA), string(uidC)} // uidB stripped
	state := wideOpenState()
	state.Positions[uidA] = Position{QtyLots: 100, LastPrice: model.MustDecimal("100")} // backs the sell
	actions := []model.Decision{
		buy(uidA, 1),
		buy(uidB, 1), // stripped
		hold(uidC),
		sell(uidA, 1),
		buy(uidC, 1),
	}
	res := Check(actions, state, limits)

	wantUIDs := []model.InstrumentUID{uidA, uidC, uidA, uidC}
	if len(res.Allowed) != len(wantUIDs) {
		t.Fatalf("Allowed = %+v, want %d entries", res.Allowed, len(wantUIDs))
	}
	for i, want := range wantUIDs {
		if res.Allowed[i].InstrumentUID != want {
			t.Errorf("Allowed[%d].InstrumentUID = %s, want %s (original relative order)", i, res.Allowed[i].InstrumentUID, want)
		}
	}
}

func TestCheck_Determinism(t *testing.T) {
	actions := []model.Decision{buy(uidA, 20), sell(uidB, 3), hold(uidC), buy(uidC, 50)}
	limits := wideOpenLimits()
	limits.MaxPositionNotional = "1000"
	limits.CashFloor = "500"
	state := wideOpenState()
	state.Cash = model.MustDecimal("5000")

	first := Check(actions, state, limits)
	for i := 0; i < 20; i++ {
		got := Check(actions, state, limits)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs from the first run:\n got  %+v\n want %+v", i, got, first)
		}
	}
}

func TestCheck_ConcurrentCallsAreRace_Free(t *testing.T) {
	actions := []model.Decision{buy(uidA, 20), sell(uidB, 3), buy(uidC, 50)}
	limits := wideOpenLimits()
	limits.MaxPositionNotional = "1000"
	state := wideOpenState()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Check(actions, state, limits)
		}()
	}
	wg.Wait()
}
