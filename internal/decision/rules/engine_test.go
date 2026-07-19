package rules

import (
	"context"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/decision"
	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/model"
)

var asOf = time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC)

// instrumentFixture builds a single-instrument InstrumentContext with the
// given feature values, everything else at reasonable defaults.
func instrumentFixture(uid model.InstrumentUID, emaFast, emaSlow, rsi, atr float64, lastClose string) decision.InstrumentContext {
	return decision.InstrumentContext{
		UID:               uid,
		Ticker:            string(uid),
		Lot:               10,
		MinPriceIncrement: model.MustDecimal("0.01"),
		Quote: decision.QuoteView{
			Bid: model.MustDecimal(lastClose), Ask: model.MustDecimal(lastClose), Last: model.MustDecimal(lastClose), TS: asOf,
		},
		Features: features.Snapshot{
			UID:       uid,
			Interval:  model.Interval5m,
			AsOf:      asOf,
			LastClose: model.MustDecimal(lastClose),
			EMAFast:   emaFast,
			EMASlow:   emaSlow,
			RSI:       rsi,
			ATR:       atr,
			Params:    features.DefaultParams(),
		},
		DataFreshness: 30 * time.Second,
	}
}

func baseRequest() decision.Request {
	return decision.Request{
		AsOf: asOf,
		Mode: "paper",
		Portfolio: decision.Portfolio{
			Cash:   model.MustDecimal("1000000"),
			Equity: model.MustDecimal("1000000"),
		},
		Limits: decision.Limits{
			MaxPositionNotional: model.MustDecimal("1000000"),
		},
	}
}

func decideOne(t *testing.T, req decision.Request) model.Decision {
	t.Helper()
	eng, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, _, err := eng.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(resp.Actions) != 1 {
		t.Fatalf("Actions len = %d, want 1 (resp=%+v)", len(resp.Actions), resp)
	}
	return resp.Actions[0]
}

func TestDecide_EntersOnBullishCrossWithConfirmingRSI(t *testing.T) {
	req := baseRequest()
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"),
	}
	got := decideOne(t, req)
	if got.Action != model.ActionBuy {
		t.Fatalf("Action = %v, want buy (rationale=%q)", got.Action, got.Rationale)
	}
	if got.Quantity <= 0 {
		t.Fatalf("Quantity = %d, want > 0", got.Quantity)
	}
	if got.OrderType != model.OrderMarket {
		t.Errorf("OrderType = %v, want market", got.OrderType)
	}
	if got.LimitPrice != nil {
		t.Errorf("LimitPrice = %v, want nil for a market order", got.LimitPrice)
	}
}

func TestDecide_NoEntryWhenAlreadyPositioned(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Positions = []decision.PositionView{{UID: "SBER-UID", Qty: 5}}
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"),
	}
	got := decideOne(t, req)
	if got.Action != model.ActionHold {
		t.Fatalf("Action = %v, want hold when already positioned (rationale=%q)", got.Action, got.Rationale)
	}
}

func TestDecide_SuppressesEntryWhileIntentOpen(t *testing.T) {
	req := baseRequest()
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"), // bullish entry signal
	}
	// A resting (acked) buy intent for the same instrument: a fresh entry must
	// be suppressed rather than stacking a second buy on the working order.
	req.OpenIntents = []decision.IntentView{{
		ClientOrderID: "co-1", InstrumentUID: "SBER-UID", Side: model.SideBuy, Qty: 3,
		Type: model.OrderMarket, TimeInForce: model.TIFDay, State: model.IntentAcked,
	}}

	got := decideOne(t, req)
	if got.Action != model.ActionHold {
		t.Fatalf("Action = %v, want hold while an intent is working (rationale=%q)", got.Action, got.Rationale)
	}
}

func TestDecide_SuppressesExitWhileIntentOpen(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Positions = []decision.PositionView{{UID: "SBER-UID", Qty: 5}}
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 240, 245, 55, 2.5, "250"), // bearish: would normally close
	}
	req.OpenIntents = []decision.IntentView{{
		ClientOrderID: "co-2", InstrumentUID: "SBER-UID", Side: model.SideSell, Qty: 5,
		Type: model.OrderMarket, TimeInForce: model.TIFDay, State: model.IntentSubmitted,
	}}

	got := decideOne(t, req)
	if got.Action != model.ActionHold {
		t.Fatalf("Action = %v, want hold while an exit intent is working (rationale=%q)", got.Action, got.Rationale)
	}
}

func TestDecide_TerminalIntentDoesNotSuppress(t *testing.T) {
	req := baseRequest()
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"),
	}
	// A filled (terminal) intent is not "working" and must not suppress a fresh
	// signal — otherwise every past order would block the instrument forever.
	req.OpenIntents = []decision.IntentView{{
		ClientOrderID: "co-3", InstrumentUID: "SBER-UID", Side: model.SideBuy, Qty: 3,
		Type: model.OrderMarket, TimeInForce: model.TIFDay, State: model.IntentFilled,
	}}

	got := decideOne(t, req)
	if got.Action != model.ActionBuy {
		t.Fatalf("Action = %v, want buy (a terminal intent must not suppress) rationale=%q", got.Action, got.Rationale)
	}
}

func TestDecide_NoEntryWhenRSIOutsideBand(t *testing.T) {
	cases := []struct {
		name string
		rsi  float64
	}{
		{"at lower bound (exclusive)", 50},
		{"below band", 40},
		{"at upper bound (exclusive)", 70},
		{"above band", 85},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			req.Instruments = []decision.InstrumentContext{
				instrumentFixture("SBER-UID", 251, 245, tc.rsi, 2.5, "250"),
			}
			got := decideOne(t, req)
			if got.Action != model.ActionHold {
				t.Fatalf("Action = %v, want hold at rsi=%v", got.Action, tc.rsi)
			}
		})
	}
}

func TestDecide_ExitsOnBearishCross(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Positions = []decision.PositionView{{UID: "SBER-UID", Qty: 5}}
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 240, 245, 55, 2.5, "250"),
	}
	got := decideOne(t, req)
	if got.Action != model.ActionClose {
		t.Fatalf("Action = %v, want close (rationale=%q)", got.Action, got.Rationale)
	}
	if got.Quantity != 0 || got.LimitPrice != nil {
		t.Errorf("close action must carry no qty/limit: qty=%d limit=%v", got.Quantity, got.LimitPrice)
	}
}

func TestDecide_ExitsOnOverboughtRSI(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Positions = []decision.PositionView{{UID: "SBER-UID", Qty: 5}}
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 85, 2.5, "250"), // still bullish EMA, but RSI > 80
	}
	got := decideOne(t, req)
	if got.Action != model.ActionClose {
		t.Fatalf("Action = %v, want close on RSI overbought (rationale=%q)", got.Action, got.Rationale)
	}
}

func TestDecide_HoldsWithPositionAndNoExitSignal(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Positions = []decision.PositionView{{UID: "SBER-UID", Qty: 5}}
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"),
	}
	got := decideOne(t, req)
	if got.Action != model.ActionHold {
		t.Fatalf("Action = %v, want hold", got.Action)
	}
}

func TestDecide_SkipsStaleData(t *testing.T) {
	req := baseRequest()
	stale := instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250")
	stale.DataFreshness = 2 * time.Hour
	req.Instruments = []decision.InstrumentContext{stale}

	eng, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, _, err := eng.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(resp.Actions) != 0 {
		t.Fatalf("Actions = %+v, want none for a stale-skipped instrument", resp.Actions)
	}
	if resp.Notes == "" {
		t.Error("Notes should explain the stale-data skip")
	}
}

func TestDecide_SizingFlooring(t *testing.T) {
	// Equity 1,000,000; RiskFractionBps 100 (1%) -> risk_budget 10,000.
	// ATR 2.5 * multiplier 2 -> risk_per_share 5. shares = 10000/5 = 2000.
	// lot 10 -> 200 lots exactly, no flooring needed at this point; pick
	// numbers that force a fractional lot count to confirm floor(), not
	// round().
	req := baseRequest()
	req.Portfolio.Cash = model.MustDecimal("100000000")
	req.Limits.MaxPositionNotional = model.MustDecimal("100000000")
	// risk_budget = 10000; risk_per_share = 2.5*2=5; shares=2000;
	// lot=13 -> 2000/13 = 153.84 -> floor 153.
	instr := instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250")
	instr.Lot = 13
	req.Instruments = []decision.InstrumentContext{instr}

	got := decideOne(t, req)
	if got.Action != model.ActionBuy {
		t.Fatalf("Action = %v, want buy (rationale=%q)", got.Action, got.Rationale)
	}
	if got.Quantity != 153 {
		t.Fatalf("Quantity = %d, want 153 (floor of 2000/13)", got.Quantity)
	}
}

func TestDecide_CapsByMaxPositionNotional(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Cash = model.MustDecimal("100000000")
	// Uncapped sizing would be far larger than this notional cap.
	req.Limits.MaxPositionNotional = model.MustDecimal("2500") // 10 lots * 250 = 2500 -> 10 lots max
	instr := instrumentFixture("SBER-UID", 251, 245, 60, 0.1, "250")
	req.Instruments = []decision.InstrumentContext{instr}

	got := decideOne(t, req)
	if got.Action != model.ActionBuy {
		t.Fatalf("Action = %v, want buy (rationale=%q)", got.Action, got.Rationale)
	}
	if got.Quantity != 1 {
		t.Fatalf("Quantity = %d, want 1 (2500 notional / 250 price / lot 10 = 1 lot)", got.Quantity)
	}
}

func TestDecide_CapsByAvailableCash(t *testing.T) {
	req := baseRequest()
	req.Limits.MaxPositionNotional = model.MustDecimal("100000000")
	req.Portfolio.Cash = model.MustDecimal("2500") // 1 lot at 250*10
	instr := instrumentFixture("SBER-UID", 251, 245, 60, 0.1, "250")
	req.Instruments = []decision.InstrumentContext{instr}

	got := decideOne(t, req)
	if got.Action != model.ActionBuy {
		t.Fatalf("Action = %v, want buy (rationale=%q)", got.Action, got.Rationale)
	}
	if got.Quantity != 1 {
		t.Fatalf("Quantity = %d, want 1 (cash-capped)", got.Quantity)
	}
}

func TestDecide_ZeroCashSkipsToHold(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Cash = model.MustDecimal("0")
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"),
	}
	got := decideOne(t, req)
	if got.Action != model.ActionHold {
		t.Fatalf("Action = %v, want hold when sizing floors to zero", got.Action)
	}
}

func TestDecide_ConfidenceInRange(t *testing.T) {
	req := baseRequest()
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 65, 2.5, "250"),
	}
	got := decideOne(t, req)
	if got.Confidence < 0 || got.Confidence > 1 {
		t.Fatalf("Confidence = %v, out of [0,1]", got.Confidence)
	}
	if got.Confidence <= DefaultParams().ConfidenceBase {
		t.Errorf("Confidence = %v, want > base %v for a nontrivial RSI distance from 50", got.Confidence, DefaultParams().ConfidenceBase)
	}
}

func TestDecide_ValidatesShapeAndSemantics(t *testing.T) {
	req := baseRequest()
	req.Portfolio.Positions = []decision.PositionView{{UID: "GAZP-UID", Qty: 5}}
	req.Instruments = []decision.InstrumentContext{
		instrumentFixture("SBER-UID", 251, 245, 60, 2.5, "250"),  // entry
		instrumentFixture("GAZP-UID", 240, 245, 55, 3.0, "140"),  // exit
		instrumentFixture("LKOH-UID", 100, 105, 40, 5.0, "6000"), // hold
	}

	eng, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, _, err := eng.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if errs := decision.ValidateShape(resp); len(errs) != 0 {
		t.Fatalf("ValidateShape errors: %+v", errs)
	}
	if errs := decision.ValidateSemantics(resp, req); len(errs) != 0 {
		t.Fatalf("ValidateSemantics errors: %+v", errs)
	}
}

func TestDecide_RespectsCancelledContext(t *testing.T) {
	req := baseRequest()
	eng, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := eng.Decide(ctx, req); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

func TestDecide_UsesInjectedClockForDuration(t *testing.T) {
	sim := clock.NewSimulated(asOf)
	eng, err := New(DefaultParams(), sim)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := baseRequest()
	_, meta, err := eng.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if meta.DurationMS != 0 {
		t.Errorf("DurationMS = %d, want 0 with a simulated clock that never advances", meta.DurationMS)
	}
}

func TestNew_RejectsInvalidParams(t *testing.T) {
	bad := DefaultParams()
	bad.ATRMultiplier = 0
	if _, err := New(bad, nil); err == nil {
		t.Fatal("expected an error for an invalid atr_multiplier")
	}
}

func TestEngine_NameAndVersion(t *testing.T) {
	eng, err := New(DefaultParams(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if eng.Name() != "rules" {
		t.Errorf("Name() = %q, want %q", eng.Name(), "rules")
	}
	if eng.Version() != "rules/v1" {
		t.Errorf("Version() = %q, want %q", eng.Version(), "rules/v1")
	}
}
