package paper

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/execution"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

var openSession execution.Session // zero value => 24h open

// TestSubmit_NextObservationDiscipline proves Submit never fills and, past that,
// that the very observation the order was submitted against cannot fill it: only
// a strictly later observation does. This is the discipline that stops a fill
// against a quote observed no later than the decision it acts on.
func TestSubmit_NextObservationDiscipline(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0")
	instr := seedInstrument(t, db, "uid-1", 10, "0.01")

	q0 := quote("uid-1", "99.90", "100.00", "99.95", 0) // the submission observation
	id := submit(t, s, db, buyMarket("uid-1", 2), instr, q0, openSession)

	if got := stateOf(t, db, id); got != model.IntentAcked {
		t.Fatalf("state after Submit = %s, want acked (Submit must not fill)", got)
	}
	if fs := fillsOf(t, db, id); len(fs) != 0 {
		t.Fatalf("Submit produced %d fills, want 0", len(fs))
	}
	if len(applier.recorded()) != 0 {
		t.Fatalf("Submit invoked the applier %d times, want 0", len(applier.recorded()))
	}

	// Re-offering the submission observation must not fill it: it is not a
	// later observation than the one the order was activated on.
	if err := s.OnQuote(ctx, q0); err != nil {
		t.Fatalf("OnQuote same observation: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentAcked {
		t.Fatalf("state after the submission observation = %s, want acked (must not fill)", got)
	}
	if len(fillsOf(t, db, id)) != 0 {
		t.Fatal("the submission observation produced a fill")
	}

	// A strictly later observation fills it.
	q1 := quote("uid-1", "99.90", "100.00", "99.95", time.Second)
	if err := observe(t, s, clk, q1); err != nil {
		t.Fatalf("OnQuote next observation: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentFilled {
		t.Fatalf("state after the next observation = %s, want filled", got)
	}
	if fs := fillsOf(t, db, id); len(fs) != 1 {
		t.Fatalf("got %d fills, want 1", len(fs))
	}
}

// TestOnQuote_MarketBuyFillPriceAndCommission checks the end-to-end fill price,
// quantity, commission and the payload handed to the portfolio.
func TestOnQuote_MarketBuyFillPriceAndCommission(t *testing.T) {
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0.0005")
	instr := seedInstrument(t, db, "uid-1", 10, "0.01")

	q0 := quote("uid-1", "99.90", "100.00", "99.95", 0)
	id := submit(t, s, db, buyMarket("uid-1", 2), instr, q0, openSession)
	fillTS := base.Add(time.Second)
	q1 := quote("uid-1", "99.90", "100.00", "99.95", time.Second) // the next observation
	if err := observe(t, s, clk, q1); err != nil {
		t.Fatalf("OnQuote: %v", err)
	}

	fs := fillsOf(t, db, id)
	if len(fs) != 1 {
		t.Fatalf("got %d fills, want 1", len(fs))
	}
	f := fs[0]
	// price = ask 100.00; notional = 100 × 2 lots × 10 shares = 2000; fee = 1.00.
	if f.Price.Cmp(model.MustDecimal("100.00")) != 0 {
		t.Errorf("fill price = %s, want 100", f.Price)
	}
	if f.Qty != 2 {
		t.Errorf("fill qty = %d, want 2", f.Qty)
	}
	if f.Fee.Cmp(model.MustDecimal("1.00")) != 0 {
		t.Errorf("fill fee = %s, want 1", f.Fee)
	}
	if !f.TS.Equal(fillTS) {
		t.Errorf("fill ts = %s, want %s (clock time at the filling observation)", f.TS, fillTS)
	}
	if f.LowFidelity {
		t.Error("fills.low_fidelity = true, want false (priced from a real ask, not the last-price fallback)")
	}
	if f.RealizedPnL != nil {
		t.Errorf("fills.realized_pnl = %v, want nil (a buy fill, and execution never sets it)", f.RealizedPnL)
	}

	rec := applier.recorded()
	if len(rec) != 1 {
		t.Fatalf("applier called %d times, want 1", len(rec))
	}
	fa := rec[0]
	if fa.Side != model.SideBuy || fa.Lot != 10 || fa.InstrumentUID != "uid-1" || fa.LowFidelity {
		t.Errorf("FillApplication = %+v, want side=buy lot=10 uid=uid-1 lowFi=false", fa)
	}
	if fa.Fill.IntentID != id {
		t.Errorf("FillApplication.Fill.IntentID = %s, want %s", fa.Fill.IntentID, id)
	}
}

// TestOnQuote_RestingLimitCrossesLater proves a limit order rests until a quote
// crosses it.
func TestOnQuote_RestingLimitCrossesLater(t *testing.T) {
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0")
	instr := seedInstrument(t, db, "uid-1", 1, "0.01")

	// Limit buy at 100.00 with the ask above it: does not cross. A later
	// observation with the ask still above the limit keeps it resting.
	q0 := quote("uid-1", "100.05", "100.10", "100.07", 0) // submission observation
	id := submit(t, s, db, buyLimit("uid-1", 1, "100.00"), instr, q0, openSession)

	q1 := quote("uid-1", "100.05", "100.10", "100.07", time.Second)
	if err := observe(t, s, clk, q1); err != nil {
		t.Fatalf("OnQuote non-cross: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentAcked {
		t.Fatalf("state after non-crossing quote = %s, want acked (rests)", got)
	}
	if len(fillsOf(t, db, id)) != 0 {
		t.Fatal("non-crossing quote produced a fill")
	}

	// Ask drops to the limit: now it crosses and fills.
	q2 := quote("uid-1", "99.95", "100.00", "99.97", 2*time.Second)
	if err := observe(t, s, clk, q2); err != nil {
		t.Fatalf("OnQuote cross: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentFilled {
		t.Fatalf("state after crossing quote = %s, want filled", got)
	}
	fs := fillsOf(t, db, id)
	if len(fs) != 1 || fs[0].Price.Cmp(model.MustDecimal("100.00")) != 0 {
		t.Fatalf("fill = %+v, want one fill at 100", fs)
	}
}

// TestOnQuote_StaleQuoteRests: an observation older than the freshness window
// does not fill a resting day order, even when it is a valid later observation.
func TestOnQuote_StaleQuoteRests(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0") // one-minute freshness window
	instr := seedInstrument(t, db, "uid-1", 1, "0.01")

	fresh := quote("uid-1", "99.90", "100.00", "99.95", 0)
	id := submit(t, s, db, buyMarket("uid-1", 1), instr, fresh, openSession)

	// Advance the clock well past the freshness window, then offer an
	// observation that is after activation but three minutes older than "now"
	// — a strictly-later observation that is nonetheless too stale to act on.
	clk.Advance(5 * time.Minute)
	stale := quote("uid-1", "99.90", "100.00", "99.95", 2*time.Minute) // TS base+2m, now base+5m => 3m old
	if err := s.OnQuote(ctx, stale); err != nil {
		t.Fatalf("OnQuote stale: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentAcked {
		t.Fatalf("state after stale quote = %s, want acked (rests)", got)
	}
	if len(fillsOf(t, db, id)) != 0 {
		t.Fatal("stale quote produced a fill")
	}
}

// TestOnQuote_OutsideSessionRests: a quote outside the session window does not
// fill.
func TestOnQuote_OutsideSessionRests(t *testing.T) {
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0")
	instr := seedInstrument(t, db, "uid-1", 1, "0.01")

	// Session opens an hour after submission. A later observation (30 min in)
	// is a valid next observation but still before the session opens, so it
	// must not fill.
	sess := execution.Session{Start: base.Add(time.Hour), End: base.Add(2 * time.Hour)}
	q0 := quote("uid-1", "99.90", "100.00", "99.95", 0)
	id := submit(t, s, db, buyMarket("uid-1", 1), instr, q0, sess)

	later := quote("uid-1", "99.90", "100.00", "99.95", 30*time.Minute)
	if err := observe(t, s, clk, later); err != nil {
		t.Fatalf("OnQuote out of session: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentAcked {
		t.Fatalf("state after out-of-session quote = %s, want acked (rests)", got)
	}
	if len(fillsOf(t, db, id)) != 0 {
		t.Fatal("out-of-session quote produced a fill")
	}
}

// TestSubmit_RejectsBadInstrumentData journals the intent, then rejects it when
// the instrument's tick is unknown, recording the reason both as an event and
// on the intent row itself (order_intents.reason).
func TestSubmit_RejectsBadInstrumentData(t *testing.T) {
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0")
	real := seedInstrument(t, db, "uid-1", 10, "0.01") // real row for the FKs

	// Context carries the same instrument but with an unknown (zero) tick.
	badTick := model.Instrument{InstrumentRef: real.InstrumentRef, Lot: 10, Currency: "rub", Name: "n"}
	id := submit(t, s, db, buyMarket("uid-1", 1), badTick, quote("uid-1", "99.90", "100.00", "99.95", 0), openSession)

	if got := stateOf(t, db, id); got != model.IntentRejected {
		t.Fatalf("state = %s, want rejected", got)
	}
	if got := reasonEventCount(t, db, "order_rejected", id); got != 1 {
		t.Fatalf("order_rejected events for %s = %d, want 1", id, got)
	}
	in, err := (sqlite.IntentRepo{}).Get(context.Background(), db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if in.Reason == "" {
		t.Error("order_intents.reason is empty, want the rejection prose persisted on the row")
	}
}

// TestSubmit_UUIDUniqueAcrossDecisions: distinct intents get distinct ids.
func TestSubmit_UUIDUniqueAcrossDecisions(t *testing.T) {
	db := openDB(t)
	clk := clock.NewSimulated(base)
	s := newSim(t, db, clk, &fakeApplier{}, 0, "0")
	a := seedInstrument(t, db, "uid-a", 1, "0.01")
	b := seedInstrument(t, db, "uid-b", 1, "0.01")

	idA := submit(t, s, db, buyMarket("uid-a", 1), a, quote("uid-a", "10", "10.01", "10", 0), openSession)
	idB := submit(t, s, db, buyMarket("uid-b", 1), b, quote("uid-b", "20", "20.01", "20", 0), openSession)
	if idA == idB {
		t.Fatalf("two intents share a client order id %q", idA)
	}
}

// TestSubmit_HoldProducesNoIntent: a hold is not actionable.
func TestSubmit_HoldProducesNoIntent(t *testing.T) {
	db := openDB(t)
	clk := clock.NewSimulated(base)
	s := newSim(t, db, clk, &fakeApplier{}, 0, "0")
	instr := seedInstrument(t, db, "uid-1", 1, "0.01")
	decID := seedDecision(t, db, instr.UID)

	d := model.Decision{InstrumentUID: instr.UID, Action: model.ActionHold, OrderType: model.OrderMarket, TimeInForce: model.TIFDay}
	sc := execution.SubmitContext{
		Instruments: map[model.InstrumentUID]execution.InstrumentContext{instr.UID: {Instrument: instr, Quote: quote(instr.UID, "10", "10.01", "10", 0)}},
		DecisionIDs: []int64{decID},
		Session:     openSession,
	}
	if err := s.Submit(context.Background(), []model.Decision{d}, sc); err != nil {
		t.Fatalf("Submit hold: %v", err)
	}
	if rows := loadIntents(t, db); len(rows) != 0 {
		t.Fatalf("hold produced %d intents, want 0", len(rows))
	}
}

// TestSettle_RollbackOnApplierError: a failing portfolio applier rolls back the
// whole fill — the intent stays acked and no fill row lands.
func TestSettle_RollbackOnApplierError(t *testing.T) {
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{err: errors.New("portfolio boom")}
	s := newSim(t, db, clk, applier, 0, "0.0005")
	instr := seedInstrument(t, db, "uid-1", 10, "0.01")

	q0 := quote("uid-1", "99.90", "100.00", "99.95", 0)
	id := submit(t, s, db, buyMarket("uid-1", 1), instr, q0, openSession)

	q1 := quote("uid-1", "99.90", "100.00", "99.95", time.Second) // next observation reaches settle
	err := observe(t, s, clk, q1)
	if err == nil {
		t.Fatal("OnQuote returned nil, want the applier error surfaced")
	}
	// The applier was invoked inside the transaction (recorded), but the tx
	// rolled back.
	if len(applier.recorded()) != 1 {
		t.Fatalf("applier calls = %d, want 1 (invoked then rolled back)", len(applier.recorded()))
	}
	if got := stateOf(t, db, id); got != model.IntentAcked {
		t.Fatalf("state after rollback = %s, want acked", got)
	}
	if len(fillsOf(t, db, id)) != 0 {
		t.Fatal("a fill row survived the rollback")
	}
}

// TestOnQuote_ConcurrentFillsExactlyOnce: many OnQuote calls racing on one
// resting order fill it exactly once; the compare-and-swap surfaces the losers
// as StateConflictError and never double-fills.
func TestOnQuote_ConcurrentFillsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0")
	instr := seedInstrument(t, db, "uid-1", 1, "0.01")

	q0 := quote("uid-1", "99.90", "100.00", "99.95", 0)
	id := submit(t, s, db, buyMarket("uid-1", 1), instr, q0, openSession)

	// Advance the clock once so the racing observation is a valid next
	// observation, then race many OnQuote calls against that single quote.
	q1 := quote("uid-1", "99.90", "100.00", "99.95", time.Second)
	clk.Advance(time.Second)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.OnQuote(ctx, q1)
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err == nil {
			continue
		}
		var conflict sqlite.StateConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("OnQuote error = %v, want nil or StateConflictError", err)
		}
	}
	if got := stateOf(t, db, id); got != model.IntentFilled {
		t.Fatalf("final state = %s, want filled", got)
	}
	if fs := fillsOf(t, db, id); len(fs) != 1 {
		t.Fatalf("got %d fills, want exactly 1", len(fs))
	}
	if len(applier.recorded()) != 1 {
		t.Fatalf("applier applied %d fills, want exactly 1", len(applier.recorded()))
	}
}

// TestExpireDay_CancelsRestingDayOrders cancels resting day orders at session
// end while leaving filled ones alone.
func TestExpireDay_CancelsRestingDayOrders(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	clk := clock.NewSimulated(base)
	applier := &fakeApplier{}
	s := newSim(t, db, clk, applier, 0, "0")
	a := seedInstrument(t, db, "uid-a", 1, "0.01")
	b := seedInstrument(t, db, "uid-b", 1, "0.01")

	// A: a market buy that fills before expiry, on a later observation.
	qa := quote("uid-a", "9.99", "10.00", "9.99", 0)
	idFilled := submit(t, s, db, buyMarket("uid-a", 1), a, qa, openSession)
	if err := observe(t, s, clk, quote("uid-a", "9.99", "10.00", "9.99", time.Second)); err != nil {
		t.Fatalf("OnQuote A: %v", err)
	}

	// B: a limit buy that never crosses, so it rests until expiry.
	qb := quote("uid-b", "20.05", "20.10", "20.07", 0)
	idResting := submit(t, s, db, buyLimit("uid-b", 1, "20.00"), b, qb, openSession)
	if err := observe(t, s, clk, quote("uid-b", "20.05", "20.10", "20.07", 2*time.Second)); err != nil {
		t.Fatalf("OnQuote B: %v", err)
	}
	if got := stateOf(t, db, idResting); got != model.IntentAcked {
		t.Fatalf("B state = %s, want acked before expiry", got)
	}

	if err := s.ExpireDay(ctx, base.Add(6*time.Hour)); err != nil {
		t.Fatalf("ExpireDay: %v", err)
	}
	if got := stateOf(t, db, idResting); got != model.IntentCanceled {
		t.Errorf("resting order after expiry = %s, want canceled", got)
	}
	if got := stateOf(t, db, idFilled); got != model.IntentFilled {
		t.Errorf("filled order after expiry = %s, want filled (untouched)", got)
	}
	expired, err := (sqlite.IntentRepo{}).Get(ctx, db, idResting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if expired.Reason != "day order expired" {
		t.Errorf("expired order reason = %q, want %q persisted on the row", expired.Reason, "day order expired")
	}
}

// TestOnQuote_IOCUnfilledCancels: an immediate-or-cancel order is canceled when
// it does not cross on its first genuine next observation — but the observation
// it was submitted against must not cancel it (a pre-decision quote must neither
// fill nor cancel).
func TestOnQuote_IOCUnfilledCancels(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	clk := clock.NewSimulated(base)
	s := newSim(t, db, clk, &fakeApplier{}, 0, "0")
	instr := seedInstrument(t, db, "uid-1", 1, "0.01")

	d := model.Decision{InstrumentUID: instr.UID, Action: model.ActionBuy, Quantity: 1, OrderType: model.OrderLimit, LimitPrice: decPtr("100.00"), TimeInForce: model.TIFIOC}
	q0 := quote("uid-1", "100.05", "100.10", "100.07", 0) // submission observation, above the limit
	id := submit(t, s, db, d, instr, q0, openSession)

	// The submission observation must leave the IOC resting, not cancel it.
	if err := s.OnQuote(ctx, q0); err != nil {
		t.Fatalf("OnQuote same observation: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentAcked {
		t.Fatalf("IOC state after the submission observation = %s, want acked (must not cancel yet)", got)
	}

	// Its first genuine next observation still does not cross: now it cancels.
	q1 := quote("uid-1", "100.05", "100.10", "100.07", time.Second)
	if err := observe(t, s, clk, q1); err != nil {
		t.Fatalf("OnQuote next observation: %v", err)
	}
	if got := stateOf(t, db, id); got != model.IntentCanceled {
		t.Fatalf("IOC state after a non-crossing next observation = %s, want canceled", got)
	}
	in, err := (sqlite.IntentRepo{}).Get(ctx, db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(in.Reason, "ioc") {
		t.Errorf("canceled IOC reason = %q, want the ioc-unfilled prose persisted on the row", in.Reason)
	}
}

// TestDeterminism_SameScriptSameFills runs an identical script twice and checks
// the produced fills match field-for-field (client order ids differ, being
// random, so they are excluded from the comparison).
func TestDeterminism_SameScriptSameFills(t *testing.T) {
	run := func() []execution.FillApplication {
		db := openDB(t)
		clk := clock.NewSimulated(base)
		applier := &fakeApplier{}
		s := newSim(t, db, clk, applier, 25, "0.0005")
		instr := seedInstrument(t, db, "uid-1", 10, "0.01")

		// Market buy fills on its next observation; limit sell rests then crosses.
		qBuy := quote("uid-1", "99.90", "100.00", "99.95", 0)
		submit(t, s, db, buyMarket("uid-1", 3), instr, qBuy, openSession)
		mustObserve(t, s, clk, quote("uid-1", "99.90", "100.00", "99.95", time.Second))

		submit(t, s, db, sellLimit("uid-1", 2, "101.00"), instr, qBuy, openSession)
		mustObserve(t, s, clk, quote("uid-1", "100.50", "100.60", "100.55", 2*time.Second)) // no cross
		mustObserve(t, s, clk, quote("uid-1", "101.20", "101.30", "101.25", 3*time.Second)) // crosses
		return applier.recorded()
	}

	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("run produced %d and %d fills", len(a), len(b))
	}
	for i := range a {
		if !sameFill(a[i], b[i]) {
			t.Fatalf("fill %d differs across runs:\n a=%+v\n b=%+v", i, a[i], b[i])
		}
	}
	if len(a) != 2 {
		t.Fatalf("expected 2 fills in the script, got %d", len(a))
	}
}

// TestOnQuote_PreActivationAndFutureQuotesDoNotFill proves the two quotes that
// are not a valid next observation — one stamped before the order was activated
// (a delayed pre-decision quote) and one stamped in the future — neither fill a
// day order nor cancel an IOC.
func TestOnQuote_PreActivationAndFutureQuotesDoNotFill(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	clk := clock.NewSimulated(base)
	s := newSim(t, db, clk, &fakeApplier{}, 0, "0")
	instr := seedInstrument(t, db, "uid-1", 1, "0.01")

	// A market day buy and an IOC limit buy, both activated at base.
	submissionQuote := quote("uid-1", "99.90", "100.00", "99.95", 0)
	dayID := submit(t, s, db, buyMarket("uid-1", 1), instr, submissionQuote, openSession)
	iocDec := model.Decision{InstrumentUID: instr.UID, Action: model.ActionBuy, Quantity: 1, OrderType: model.OrderLimit, LimitPrice: decPtr("100.00"), TimeInForce: model.TIFIOC}
	iocID := submit(t, s, db, iocDec, instr, submissionQuote, openSession)

	// A quote observed one second BEFORE activation (delayed delivery). It is
	// crossable for both orders, yet must be ignored entirely.
	preDecision := quote("uid-1", "99.90", "100.00", "99.95", -time.Second)
	if err := s.OnQuote(ctx, preDecision); err != nil {
		t.Fatalf("OnQuote pre-decision: %v", err)
	}

	// A future-dated quote (the clock is still at base). Also crossable, also
	// ignored.
	future := quote("uid-1", "99.90", "100.00", "99.95", time.Hour)
	if err := s.OnQuote(ctx, future); err != nil {
		t.Fatalf("OnQuote future: %v", err)
	}

	if got := stateOf(t, db, dayID); got != model.IntentAcked {
		t.Errorf("day order state = %s, want acked (neither quote is a valid observation)", got)
	}
	if got := stateOf(t, db, iocID); got != model.IntentAcked {
		t.Errorf("ioc order state = %s, want acked (a pre-activation/future quote must not cancel it)", got)
	}
	if len(fillsOf(t, db, dayID)) != 0 || len(fillsOf(t, db, iocID)) != 0 {
		t.Fatal("a pre-activation or future quote produced a fill")
	}
}

// sameFill compares two applications ignoring the random client order id.
func sameFill(x, y execution.FillApplication) bool {
	return x.InstrumentUID == y.InstrumentUID &&
		x.Side == y.Side &&
		x.Lot == y.Lot &&
		x.LowFidelity == y.LowFidelity &&
		x.Fill.Price.Cmp(y.Fill.Price) == 0 &&
		x.Fill.Qty == y.Fill.Qty &&
		x.Fill.Fee.Cmp(y.Fill.Fee) == 0 &&
		x.Fill.TS.Equal(y.Fill.TS)
}

// reasonEventCount counts events with the given code whose payload names the
// client order id — proof the rejection reason was persisted durably.
func reasonEventCount(t *testing.T, db *sqlite.DB, code, clientOrderID string) int {
	t.Helper()
	evs, err := (sqlite.EventRepo{}).Recent(context.Background(), db, 100)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	n := 0
	for _, e := range evs {
		if e.Code == code && strings.Contains(e.Payload, clientOrderID) {
			n++
		}
	}
	return n
}
