package tinvestcli

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// readEvents reads from the stream until stop(events) is true, the channel
// closes, or the timeout fires. A nil stop reads until the channel closes.
func readEvents(t *testing.T, s *Stream, stop func([]Event) bool, timeout time.Duration) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				return got
			}
			got = append(got, ev)
			if stop != nil && stop(got) {
				return got
			}
		case <-deadline:
			t.Fatalf("stream read timed out after %v with %d events: %s", timeout, len(got), kinds(got))
			return got
		}
	}
}

func kinds(evs []Event) string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = fmt.Sprintf("%T", e)
	}
	return fmt.Sprint(out)
}

func countCandles(evs []Event) (n int) {
	for _, e := range evs {
		if _, ok := e.(CandleEvent); ok {
			n++
		}
	}
	return
}

func countLastPrices(evs []Event) (n int) {
	for _, e := range evs {
		if _, ok := e.(LastPriceEvent); ok {
			n++
		}
	}
	return
}

func countOrderbooks(evs []Event) (n int) {
	for _, e := range evs {
		if _, ok := e.(OrderbookEvent); ok {
			n++
		}
	}
	return
}

func countConnectedAttempt1(evs []Event) (n int) {
	for _, e := range evs {
		if se, ok := e.(StatusEvent); ok && se.Kind == StatusConnected && se.Attempt == 1 {
			n++
		}
	}
	return
}

func countGaps(evs []Event) (n int) {
	for _, e := range evs {
		if _, ok := e.(GapEvent); ok {
			n++
		}
	}
	return
}

func TestStreamRequiresInstrument(t *testing.T) {
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), nil)
	_, err := c.StreamMarketdata(context.Background(), StreamRequest{})
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want *UsageError for no instruments, got %T: %v", err, err)
	}
}

func TestStreamHappyPlayback(t *testing.T) {
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), streamFast)
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Account:        "test-brokerage-0001",
		Instruments:    []string{"SBER@TQBR", "GAZP@TQBR"},
		Candles:        true,
		CandleInterval: model.Interval5m,
		LastPrice:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Read the first run's events (two candles + two last prices + connected).
	got := readEvents(t, s, func(e []Event) bool {
		return countCandles(e) >= 2 && countLastPrices(e) >= 2
	}, 5*time.Second)

	if len(got) == 0 {
		t.Fatal("no events")
	}
	first, ok := got[0].(StatusEvent)
	if !ok || first.Kind != StatusConnected || first.Attempt != 1 || first.Subscriptions != 2 {
		t.Fatalf("first event should be connected(attempt 1, subs 2): %+v", got[0])
	}

	var sberCandle *CandleEvent
	var sberPrice *LastPriceEvent
	for _, e := range got {
		switch ev := e.(type) {
		case CandleEvent:
			if ev.InstrumentUID == sberUID {
				c := ev
				sberCandle = &c
			}
		case LastPriceEvent:
			if ev.InstrumentUID == sberUID {
				p := ev
				sberPrice = &p
			}
		}
	}
	if sberCandle == nil {
		t.Fatal("no SBER candle event")
	}
	if sberCandle.Interval != "SUBSCRIPTION_INTERVAL_FIVE_MINUTES" {
		t.Fatalf("candle interval = %q", sberCandle.Interval)
	}
	eqDec(t, sberCandle.Close, "270.8")
	if sberCandle.Volume != 1200 {
		t.Fatalf("candle volume = %d", sberCandle.Volume)
	}
	if !sberCandle.CandleTime.Equal(time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("candle time = %v, want the candle_time field", sberCandle.CandleTime)
	}
	mc, err := sberCandle.Model()
	if err != nil {
		t.Fatal(err)
	}
	if mc.Interval != model.Interval5m || mc.Complete {
		t.Fatalf("model candle: %+v", mc)
	}
	eqDec(t, mc.Close, "270.8")

	if sberPrice == nil {
		t.Fatal("no SBER last-price event")
	}
	eqDec(t, sberPrice.Price, "270.8")
	if !sberPrice.Time.Equal(time.Date(2026, 7, 19, 10, 0, 30, 0, time.UTC)) {
		t.Fatalf("last price time = %v", sberPrice.Time)
	}
}

// TestStreamCandleTimeIsEventTime proves the candle event time comes from
// data.candle_time, not the frame receipt time, by making the two distinct.
func TestStreamCandleTimeIsEventTime(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:05:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"candle","time":"2026-07-19T10:05:00Z","data":{
	     "instrument_uid":"` + sberUID + `","interval":"SUBSCRIPTION_INTERVAL_FIVE_MINUTES",
	     "open":{"value":"100"},"high":{"value":"101"},"low":{"value":"99"},"close":{"value":"100.5"},
	     "volume":"10","candle_time":"2026-07-19T10:00:00Z","source":"CANDLE_SOURCE_EXCHANGE"}}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-candle-time\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	c := newClient(t, dir, t.TempDir(), streamFast)
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Instruments: []string{sberUID}, Candles: true, CandleInterval: model.Interval5m,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := readEvents(t, s, func(e []Event) bool { return countCandles(e) >= 1 }, 5*time.Second)
	var candle *CandleEvent
	for _, e := range got {
		if ev, ok := e.(CandleEvent); ok {
			candle = &ev
			break
		}
	}
	if candle == nil {
		t.Fatal("no candle event")
	}
	frameTime := time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC)
	candleTime := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	if !candle.CandleTime.Equal(candleTime) {
		t.Fatalf("CandleTime = %v, want candle_time %v", candle.CandleTime, candleTime)
	}
	if candle.CandleTime.Equal(frameTime) {
		t.Fatal("CandleTime must not be the frame receipt time")
	}
}

// TestStreamOrderbookBestBidAsk proves an order-book frame is parsed into an
// OrderbookEvent carrying top-of-book (best bid/ask), rather than being
// swallowed as an unknown lifecycle status.
func TestStreamOrderbookBestBidAsk(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"orderbook","time":"2026-07-19T10:00:00Z","data":{
	     "instrument_uid":"` + sberUID + `","ticker":"SBER","class_code":"TQBR","depth":10,"is_consistent":true,
	     "bids":[{"price":{"value":"270.4"},"quantity":"120"},{"price":{"value":"270.3"},"quantity":"200"}],
	     "asks":[{"price":{"value":"270.6"},"quantity":"90"},{"price":{"value":"270.7"},"quantity":"150"}],
	     "orderbook_time":"2026-07-19T10:00:00Z","orderbook_type":"ORDERBOOK_TYPE_EXCHANGE"}}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-ob\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	c := newClient(t, dir, t.TempDir(), streamFast)
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{Instruments: []string{sberUID}, OrderbookDepth: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := readEvents(t, s, func(e []Event) bool { return countOrderbooks(e) >= 1 }, 5*time.Second)
	var ob *OrderbookEvent
	for _, e := range got {
		if o, ok := e.(OrderbookEvent); ok {
			cp := o
			ob = &cp
			break
		}
	}
	if ob == nil {
		t.Fatalf("no orderbook event: %s", kinds(got))
	}
	if ob.Depth != 10 {
		t.Fatalf("depth = %d, want 10", ob.Depth)
	}
	eqDec(t, ob.Bid, "270.4")
	eqDec(t, ob.Ask, "270.6")
	q := ob.Quote()
	eqDec(t, q.Bid, "270.4")
	eqDec(t, q.Ask, "270.6")
	if !q.HasBidAsk() {
		t.Fatal("orderbook quote should have bid and ask")
	}
	if !q.Last.IsZero() {
		t.Fatal("orderbook quote carries no last price")
	}
}

func TestStreamHostileGapAndRestart(t *testing.T) {
	c := newClient(t, shippedScenario(t, "hostile"), t.TempDir(), func(cfg *Config) {
		streamFast(cfg)
		cfg.StreamMinHealthyRun = time.Nanosecond // every run counts healthy → never trips
		cfg.StreamMaxFastRestarts = 50
	})
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Account: "test-brokerage-0002", Instruments: []string{"SBER@TQBR"},
		Candles: true, CandleInterval: model.Interval5m, LastPrice: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// The script disconnects in-band (a gap) and ends with exit 0 (an
	// unexpected process death → supervised restart, replaying connected@1).
	got := readEvents(t, s, func(e []Event) bool {
		return countGaps(e) >= 1 && countConnectedAttempt1(e) >= 2
	}, 5*time.Second)

	if countGaps(got) < 1 {
		t.Fatalf("expected at least one GapEvent, got: %s", kinds(got))
	}
	if countConnectedAttempt1(got) < 2 {
		t.Fatalf("expected a supervised restart (>=2 connected@1), got: %s", kinds(got))
	}
	// Disconnect/exit gaps are whole-subscription with a zero From: each
	// instrument lags at its own point, so the collector must backfill each
	// from its own watermark, never a single global candle time.
	for _, e := range got {
		if g, ok := e.(GapEvent); ok {
			if g.InstrumentUID != "" {
				t.Fatalf("stream-wide gap should carry no instrument, got %q", g.InstrumentUID)
			}
			if !g.From.IsZero() {
				t.Fatalf("stream-wide gap From = %v, want zero (per-instrument watermark)", g.From)
			}
		}
	}
}

func TestStreamCircuitBreaker(t *testing.T) {
	// A stream that dies immediately on every spawn must trip the breaker.
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-cb\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   `[{"exit":1}]`,
	})
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		streamFast(cfg)
		cfg.StreamMinHealthyRun = time.Second // instant deaths always count as fast
		cfg.StreamMaxFastRestarts = 3
	})
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Instruments: []string{sberUID}, Candles: true, CandleInterval: model.Interval1m,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := readEvents(t, s, nil, 5*time.Second) // read until the channel closes
	if len(got) == 0 {
		t.Fatal("no events")
	}
	last := got[len(got)-1]
	down, ok := last.(*StreamDownError)
	if !ok {
		t.Fatalf("last event should be *StreamDownError, got %T (%s)", last, kinds(got))
	}
	if !down.CircuitTripped {
		t.Fatalf("expected CircuitTripped, got %+v", down)
	}
	if down.Attempts < 3 {
		t.Fatalf("Attempts = %d, want >= 3", down.Attempts)
	}
}

func TestStreamAuthFailureNoRestart(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"error","time":"2026-07-19T10:00:01Z","error":{"code":"AUTH","message":"token rejected","retryable":false}},
	  {"exit":3}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-auth\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		streamFast(cfg)
		cfg.StreamMaxFastRestarts = 10 // prove we stop on classification, not the breaker
	})
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Instruments: []string{sberUID}, LastPrice: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := readEvents(t, s, nil, 5*time.Second) // read until close
	if n := countConnectedAttempt1(got); n != 1 {
		t.Fatalf("auth failure must not restart: saw %d connected@1 events (%s)", n, kinds(got))
	}
	last := got[len(got)-1]
	down, ok := last.(*StreamDownError)
	if !ok {
		t.Fatalf("last event should be *StreamDownError, got %T", last)
	}
	if down.CircuitTripped {
		t.Fatal("auth failure is a classification, not a circuit trip")
	}
	var ae *AuthError
	if !errors.As(down.Err, &ae) {
		t.Fatalf("StreamDownError.Err should be *AuthError, got %T: %v", down.Err, down.Err)
	}
}

// TestStreamPumpDeliversWhenConsumerFreesCapacity proves the delivery pump hands
// a pending event to the consumer as soon as capacity frees, with no further
// producer activity — the old flush-on-next-producer-event path stranded it.
// This drives the pump directly (no subprocess) so it is deterministic: a
// channel buffered to 1 is the delivery bottleneck (the queue holds both events)
// so the last price waits behind the status frame until the consumer reads.
func TestStreamPumpDeliversWhenConsumerFreesCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Stream{
		client: &Client{cfg: Config{StreamQueueSize: 8}.withDefaults()},
		events: make(chan Event, 1), // delivery bottleneck: one in flight at a time
		done:   make(chan struct{}),
		wake:   make(chan struct{}, 1),
	}
	go s.pump(ctx)

	// Two events into a queue/channel that hold one at a time, then the feed is
	// quiet: nothing else is produced.
	s.deliverStatus(StatusEvent{Kind: StatusConnected})
	s.deliverLastPrice(LastPriceEvent{InstrumentUID: sberUID, Price: model.MustDecimal("270.8")})

	first := <-s.events // read the status, freeing capacity
	if st, ok := first.(StatusEvent); !ok || st.Kind != StatusConnected {
		t.Fatalf("first event = %T %+v, want connected status", first, first)
	}
	select {
	case ev := <-s.events:
		lp, ok := ev.(LastPriceEvent)
		if !ok {
			t.Fatalf("second event = %T, want the pending LastPriceEvent", ev)
		}
		eqDec(t, lp.Price, "270.8")
	case <-time.After(2 * time.Second):
		t.Fatal("pending last price was stranded: not delivered on freed capacity while the feed was quiet")
	}
}

// TestStreamPumpDrainsGapBeforeTerminal proves a pending gap is delivered before
// the terminal StreamDownError even under backpressure and a slow consumer.
func TestStreamPumpDrainsGapBeforeTerminal(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"disconnected","time":"2026-07-19T10:00:01Z","data":{"reason":"network","final":false}},
	  {"type":"error","time":"2026-07-19T10:00:02Z","error":{"code":"AUTH","message":"token rejected","retryable":false}},
	  {"exit":3}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-drain\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		streamFast(cfg)
		cfg.StreamQueueSize = 1 // backpressure so the gap cannot ride along for free
		cfg.StreamMaxFastRestarts = 10
	})
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{Instruments: []string{sberUID}, Candles: true, CandleInterval: model.Interval5m})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A deliberately slow consumer: sleep between reads so the pump must hold
	// pending events rather than dumping them into a roomy buffer.
	var got []Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				goto done
			}
			got = append(got, ev)
			time.Sleep(15 * time.Millisecond)
		case <-deadline:
			t.Fatalf("slow-consumer read timed out with %d events: %s", len(got), kinds(got))
		}
	}
done:
	if len(got) == 0 {
		t.Fatal("no events")
	}
	last := got[len(got)-1]
	down, ok := last.(*StreamDownError)
	if !ok {
		t.Fatalf("last event should be *StreamDownError, got %T (%s)", last, kinds(got))
	}
	var ae *AuthError
	if !errors.As(down.Err, &ae) {
		t.Fatalf("terminal cause = %T, want *AuthError", down.Err)
	}
	gapIdx := -1
	for i, e := range got {
		if _, ok := e.(GapEvent); ok {
			gapIdx = i
		}
	}
	if gapIdx == -1 {
		t.Fatalf("expected a GapEvent before the terminal, got: %s", kinds(got))
	}
	if gapIdx >= len(got)-1 {
		t.Fatalf("gap must drain before the terminal event, kinds: %s", kinds(got))
	}
}

// TestPumpNeverDropsNewestMarketState proves a new last price / order book is
// enqueued even when the queue is full and holds nothing to coalesce for that
// instrument — the latest market state must survive a subsequently quiet feed.
func TestPumpNeverDropsNewestMarketState(t *testing.T) {
	s := &Stream{
		client: &Client{cfg: Config{StreamQueueSize: 2}.withDefaults()},
		events: make(chan Event, 1),
		done:   make(chan struct{}),
		wake:   make(chan struct{}, 1),
	}
	// Fill the queue to capacity with status frames (which do drop at cap).
	s.deliverStatus(StatusEvent{Kind: StatusConnected})
	s.deliverStatus(StatusEvent{Kind: StatusResubscribed})

	s.deliverLastPrice(LastPriceEvent{InstrumentUID: "u-price", Price: model.MustDecimal("42")})
	s.deliverOrderbook(OrderbookEvent{InstrumentUID: "u-book", Bid: model.MustDecimal("1"), Ask: model.MustDecimal("2")})

	s.mu.Lock()
	defer s.mu.Unlock()
	var haveP, haveB bool
	for _, e := range s.queue {
		if p, ok := e.(LastPriceEvent); ok && p.InstrumentUID == "u-price" {
			haveP = true
		}
		if b, ok := e.(OrderbookEvent); ok && b.InstrumentUID == "u-book" {
			haveB = true
		}
	}
	if !haveP {
		t.Fatal("newest last price was dropped when the queue was full")
	}
	if !haveB {
		t.Fatal("newest order book was dropped when the queue was full")
	}
}

// TestPumpCoalesceKeepsArrivalOrder proves coalescing does not deliver a newer
// event ahead of an older one: a re-queued book takes the tail position, after
// the price that arrived between the two books.
func TestPumpCoalesceKeepsArrivalOrder(t *testing.T) {
	s := &Stream{
		client: &Client{cfg: Config{StreamQueueSize: 8}.withDefaults()},
		events: make(chan Event, 1),
		done:   make(chan struct{}),
		wake:   make(chan struct{}, 1),
	}
	t1 := time.Date(2026, 7, 19, 10, 0, 1, 0, time.UTC)
	t2 := time.Date(2026, 7, 19, 10, 0, 2, 0, time.UTC)
	t3 := time.Date(2026, 7, 19, 10, 0, 3, 0, time.UTC)
	s.deliverOrderbook(OrderbookEvent{InstrumentUID: "u1", Bid: model.MustDecimal("10"), Time: t1})
	s.deliverLastPrice(LastPriceEvent{InstrumentUID: "u1", Price: model.MustDecimal("11"), Time: t2})
	s.deliverOrderbook(OrderbookEvent{InstrumentUID: "u1", Bid: model.MustDecimal("12"), Time: t3})

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) != 2 {
		t.Fatalf("queue length = %d, want 2 (one coalesced book + one price)", len(s.queue))
	}
	lp, ok := s.queue[0].(LastPriceEvent)
	if !ok || !lp.Time.Equal(t2) {
		t.Fatalf("first queued = %+v, want the price at t2 (older event first)", s.queue[0])
	}
	ob, ok := s.queue[1].(OrderbookEvent)
	if !ok || !ob.Time.Equal(t3) {
		t.Fatalf("second queued = %+v, want the coalesced book at t3 (newest last)", s.queue[1])
	}
}

// TestCloseWaitsForSupervisorTeardown proves Close does not return until the
// child is reaped. The fake ignores SIGTERM, so it only dies on the supervisor's
// SIGKILL after KillGrace; Close must block roughly that long.
func TestCloseWaitsForSupervisorTeardown(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"candle","time":"2026-07-19T10:00:00Z","delay_ms":60000,"data":{
	     "instrument_uid":"` + sberUID + `","interval":"SUBSCRIPTION_INTERVAL_FIVE_MINUTES",
	     "open":{"value":"1"},"high":{"value":"1"},"low":{"value":"1"},"close":{"value":"1"},
	     "volume":"1","candle_time":"2026-07-19T10:00:00Z"}}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-teardown\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	const killGrace = 400 * time.Millisecond
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		cfg.StreamBaseBackoff = time.Millisecond
		cfg.StreamMaxBackoff = 5 * time.Millisecond
		cfg.KillGrace = killGrace
		cfg.Env = append(cfg.Env, "FAKETINVEST_IGNORE_SIGTERM=1")
	})
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Instruments: []string{sberUID}, Candles: true, CandleInterval: model.Interval5m,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-s.Events() // connected: the child is up

	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < killGrace-100*time.Millisecond {
		t.Fatalf("Close returned in %v, before the SIGKILL teardown (KillGrace %v): supervisor teardown not awaited", elapsed, killGrace)
	}
}

// TestStreamBrokerRejectedNoRestart proves a broker rejection (exit 5) ends the
// stream terminally rather than respawning until the circuit breaker trips.
func TestStreamBrokerRejectedNoRestart(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"error","time":"2026-07-19T10:00:01Z","error":{"code":"BROKER_REJECTED","message":"instrument not tradable","retryable":false}},
	  {"exit":5}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-rej\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		streamFast(cfg)
		cfg.StreamMaxFastRestarts = 10 // prove we stop on classification, not the breaker
	})
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{Instruments: []string{sberUID}, Candles: true, CandleInterval: model.Interval5m})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := readEvents(t, s, nil, 5*time.Second)
	if n := countConnectedAttempt1(got); n != 1 {
		t.Fatalf("broker rejection must not restart: saw %d connected@1 (%s)", n, kinds(got))
	}
	last := got[len(got)-1]
	down, ok := last.(*StreamDownError)
	if !ok {
		t.Fatalf("last event = %T, want *StreamDownError (%s)", last, kinds(got))
	}
	if down.CircuitTripped {
		t.Fatal("broker rejection is a classification, not a circuit trip")
	}
	var re *BrokerRejectedError
	if !errors.As(down.Err, &re) {
		t.Fatalf("terminal cause = %T, want *BrokerRejectedError", down.Err)
	}
}

// TestStreamSnapshotBecomesOrderbook proves an authoritative "snapshot" frame is
// parsed into an OrderbookEvent (top-of-book), not swallowed as lifecycle status.
func TestStreamSnapshotBecomesOrderbook(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"snapshot","time":"2026-07-19T10:00:00Z","data":{
	     "instrument_uid":"` + sberUID + `","depth":10,
	     "bids":[{"price":{"value":"100.4"},"quantity":"5"}],
	     "asks":[{"price":{"value":"100.6"},"quantity":"5"}],
	     "last_price":{"value":"100.5"},"close_price":{"value":"99"},
	     "orderbook_time":"2026-07-19T10:00:00Z"}}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-snap\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	c := newClient(t, dir, t.TempDir(), streamFast)
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{Instruments: []string{sberUID}, OrderbookDepth: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := readEvents(t, s, func(e []Event) bool { return countOrderbooks(e) >= 1 }, 5*time.Second)
	var ob *OrderbookEvent
	for _, e := range got {
		if o, ok := e.(OrderbookEvent); ok {
			cp := o
			ob = &cp
			break
		}
	}
	if ob == nil {
		t.Fatalf("snapshot frame did not become an OrderbookEvent: %s", kinds(got))
	}
	eqDec(t, ob.Bid, "100.4")
	eqDec(t, ob.Ask, "100.6")
}

func TestStreamCleanShutdownViaClose(t *testing.T) {
	before := runtime.NumGoroutine()
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), streamFast)
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Account: "test-brokerage-0001", Instruments: []string{"SBER@TQBR"},
		Candles: true, CandleInterval: model.Interval5m, LastPrice: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Ensure the run is underway, then shut down.
	readEvents(t, s, func(e []Event) bool { return len(e) >= 1 }, 5*time.Second)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After Close the channel must be closed (drain to confirm) and Done closed.
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed after Close returned")
	}
	drainClosed(t, s)
	assertNoLeak(t, before)
}

func TestStreamShutdownViaContextCancel(t *testing.T) {
	before := runtime.NumGoroutine()
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), streamFast)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := c.StreamMarketdata(ctx, StreamRequest{
		Account: "test-brokerage-0001", Instruments: []string{"SBER@TQBR"},
		Candles: true, CandleInterval: model.Interval5m, LastPrice: true,
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	readEvents(t, s, func(e []Event) bool { return len(e) >= 1 }, 5*time.Second)
	cancel()

	// The supervisor must tear down and close the channel on ctx cancel alone.
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not shut down on context cancel")
	}
	drainClosed(t, s)
	assertNoLeak(t, before)
}

// drainClosed asserts the events channel drains and closes within a deadline.
func drainClosed(t *testing.T, s *Stream) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-s.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("events channel did not close")
		}
	}
}

// assertNoLeak polls until the goroutine count returns near the baseline,
// proving the supervisor/reader/signaler goroutines were reaped.
func assertNoLeak(t *testing.T, before int) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: baseline %d, now %d", before, runtime.NumGoroutine())
}

// streamFast tunes a client for fast, deterministic stream tests.
func streamFast(cfg *Config) {
	cfg.StreamBaseBackoff = time.Millisecond
	cfg.StreamMaxBackoff = 5 * time.Millisecond
	cfg.KillGrace = 200 * time.Millisecond
}
