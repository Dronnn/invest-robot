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

// TestStreamPumpDeliversWhenConsumerFreesCapacity proves the delivery pump
// hands a pending event to the consumer as soon as capacity frees, even while
// the feed is quiet — the old flush-on-next-producer-event path stranded it.
// The queue is capped at 1 so the last price waits behind the connected frame;
// the next producer frame (a candle) is 3s away, so a prompt delivery can only
// come from the pump reacting to freed capacity.
func TestStreamPumpDeliversWhenConsumerFreesCapacity(t *testing.T) {
	script := `[
	  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
	  {"type":"last_price","time":"2026-07-19T10:00:01Z","data":{"instrument_uid":"` + sberUID + `","price":{"value":"270.8"},"time":"2026-07-19T10:00:01Z"}},
	  {"type":"candle","time":"2026-07-19T10:00:05Z","delay_ms":3000,"data":{
	     "instrument_uid":"` + sberUID + `","interval":"SUBSCRIPTION_INTERVAL_FIVE_MINUTES",
	     "open":{"value":"1"},"high":{"value":"1"},"low":{"value":"1"},"close":{"value":"1"},
	     "volume":"1","candle_time":"2026-07-19T10:00:00Z"}}
	]`
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-pump\"\n[stream]\nscript = \"stream.json\"\n",
		"stream.json":   script,
	})
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		streamFast(cfg)
		cfg.StreamQueueSize = 1 // force the price to wait behind connected
	})
	s, err := c.StreamMarketdata(context.Background(), StreamRequest{
		Instruments: []string{sberUID}, LastPrice: true, Candles: true, CandleInterval: model.Interval5m,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	first := <-s.Events() // read connected, freeing channel capacity
	if st, ok := first.(StatusEvent); !ok || st.Kind != StatusConnected {
		t.Fatalf("first event = %T %+v, want connected", first, first)
	}
	select {
	case ev := <-s.Events():
		if _, ok := ev.(LastPriceEvent); !ok {
			t.Fatalf("second event = %T, want the pending LastPriceEvent", ev)
		}
	case <-time.After(1500 * time.Millisecond):
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
	deadline := time.After(5 * time.Second)
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
