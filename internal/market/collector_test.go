package market_test

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/market"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
)

// --- fakes ---

type fakeHandle struct {
	ch   chan tinvestcli.Event
	once sync.Once
}

func newFakeHandle() *fakeHandle {
	return &fakeHandle{ch: make(chan tinvestcli.Event, 64)}
}
func (h *fakeHandle) Events() <-chan tinvestcli.Event { return h.ch }
func (h *fakeHandle) Close() error                    { return nil }

// feed sends an event; end closes the channel to signal the stream ended.
func (h *fakeHandle) feed(ev tinvestcli.Event) { h.ch <- ev }
func (h *fakeHandle) end()                     { h.once.Do(func() { close(h.ch) }) }

type fakeBroker struct {
	mu       sync.Mutex
	insts    map[string]tinvestcli.Instrument
	candleFn func(id string, from, to time.Time) (tinvestcli.CandlesResult, error)
	handles  chan *fakeHandle
	startErr error
}

func newFakeBroker() *fakeBroker {
	return &fakeBroker{
		insts:   map[string]tinvestcli.Instrument{},
		handles: make(chan *fakeHandle, 16),
		candleFn: func(string, time.Time, time.Time) (tinvestcli.CandlesResult, error) {
			return tinvestcli.CandlesResult{}, nil
		},
	}
}

func (b *fakeBroker) InstrumentGet(_ context.Context, id string) (tinvestcli.Instrument, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	inst, ok := b.insts[id]
	if !ok {
		return tinvestcli.Instrument{}, &tinvestcli.BrokerRejectedError{
			BrokerError: tinvestcli.BrokerError{Code: "BROKER_REJECTED", Message: "instrument not found"},
		}
	}
	return inst, nil
}

func (b *fakeBroker) CandlesGet(_ context.Context, id string, _ model.CandleInterval, from, to time.Time) (tinvestcli.CandlesResult, error) {
	b.mu.Lock()
	fn := b.candleFn
	b.mu.Unlock()
	return fn(id, from, to)
}

func (b *fakeBroker) StreamMarketdata(_ context.Context, _ tinvestcli.StreamRequest) (market.StreamHandle, error) {
	if b.startErr != nil {
		return nil, b.startErr
	}
	h := newFakeHandle()
	b.handles <- h
	return h, nil
}

func (b *fakeBroker) setCandles(fn func(id string, from, to time.Time) (tinvestcli.CandlesResult, error)) {
	b.mu.Lock()
	b.candleFn = fn
	b.mu.Unlock()
}

func (b *fakeBroker) addInstrument(id, uid, ticker string) {
	b.mu.Lock()
	b.insts[id] = tinvestcli.Instrument{
		UID: uid, Ticker: ticker, ClassCode: "TQBR", Lot: 10, Currency: "rub",
		MinPriceIncrement: tinvestcli.Money{Amount: model.MustDecimal("0.01")},
	}
	b.mu.Unlock()
}

// --- helpers ---

func tempStore(t *testing.T) (*sqlite.DB, *market.SQLiteStore) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), t.TempDir()+"/market.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, market.NewSQLiteStore(db)
}

func deps(b market.Broker, s *market.SQLiteStore, clk clock.Clock) market.Deps {
	return market.Deps{Broker: b, Instruments: s, Candles: s, Quotes: s, Events: s, Clock: clk}
}

func mustCandles(t *testing.T, js string) tinvestcli.CandlesResult {
	t.Helper()
	var res tinvestcli.CandlesResult
	if err := json.Unmarshal([]byte(js), &res); err != nil {
		t.Fatalf("build candles: %v", err)
	}
	return res
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

const uidSBER = "e6123145-9665-43e0-8413-cd61b8aa9b13"

func bar(ts, o, h, l, c, vol string, complete bool) string {
	return `{"time":"` + ts + `","open":{"value":"` + o + `"},"high":{"value":"` + h +
		`"},"low":{"value":"` + l + `"},"close":{"value":"` + c + `"},"volume":"` + vol +
		`","is_complete":` + boolJSON(complete) + `}`
}
func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// --- tests ---

func TestResolveUniverseFailsClosed(t *testing.T) {
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	// "BOGUS@TQBR" is not registered → unresolved → hard start error.
	_, s := tempStore(t)
	c, err := market.New(deps(b, s, clock.Real()), market.Config{
		Universe: []string{"SBER@TQBR", "BOGUS@TQBR"}, Interval: model.Interval5m,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Start(context.Background())
	if err == nil {
		t.Fatal("expected a fail-closed start error for the unknown entry")
	}
	if !strings.Contains(err.Error(), "BOGUS@TQBR") {
		t.Fatalf("error should name the bad entry: %v", err)
	}
	c.Stop()
}

func TestStartupBackfillWritesRows(t *testing.T) {
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	b.setCandles(func(string, time.Time, time.Time) (tinvestcli.CandlesResult, error) {
		return mustCandles(t, `{"instrument_uid":"`+uidSBER+`","interval":"5m","candles":[`+
			bar("2026-07-19T09:00:00Z", "270", "271", "269.5", "270.5", "1500", true)+`,`+
			bar("2026-07-19T09:05:00Z", "270.5", "272", "270", "271.5", "1800", true)+`]}`), nil
	})
	db, s := tempStore(t)
	c, err := market.New(deps(b, s, clock.Real()), market.Config{
		Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	<-b.handles // backfill has completed by the time the stream connects

	ctx := context.Background()
	waitFor(t, "backfilled candles", func() bool {
		got, err := sqlite.CandleRepo{}.Range(ctx, db, uidSBER, model.Interval5m,
			time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC), time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC))
		return err == nil && len(got) == 2
	})
	// The instrument was persisted at resolve time.
	if _, err := (sqlite.InstrumentRepo{}).Get(ctx, db, uidSBER); err != nil {
		t.Fatalf("instrument not persisted: %v", err)
	}
	wm, ok, err := sqlite.CandleRepo{}.LatestComplete(ctx, db, uidSBER, model.Interval5m)
	if err != nil || !ok {
		t.Fatalf("watermark: ok=%v err=%v", ok, err)
	}
	if !wm.TS.Equal(time.Date(2026, 7, 19, 9, 5, 0, 0, time.UTC)) {
		t.Fatalf("watermark ts = %v", wm.TS)
	}
}

func TestRolloverConfirmMakesBarComplete(t *testing.T) {
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	t1 := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 19, 9, 5, 0, 0, time.UTC)

	// Backfill returns nothing; the unary confirm fetch returns the authoritative
	// complete bar at t1 with a close (272.0) that differs from the forming
	// stream frame's close (270.3).
	b.setCandles(func(_ string, from, _ time.Time) (tinvestcli.CandlesResult, error) {
		if from.Equal(t1) { // the confirm fetch
			return mustCandles(t, `{"instrument_uid":"`+uidSBER+`","interval":"5m","candles":[`+
				bar("2026-07-19T09:00:00Z", "270", "272.5", "269.5", "272", "5000", true)+`]}`), nil
		}
		return tinvestcli.CandlesResult{}, nil // backfill: empty
	})

	db, s := tempStore(t)
	c, _ := market.New(deps(b, s, clock.Real()), market.Config{Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	h := <-b.handles

	// Forming bar for t1 (incomplete, close 270.3), then a newer forming bar for
	// t2 which rolls t1 over and triggers the confirm.
	h.feed(formingCandle(uidSBER, t1, "270.3"))
	h.feed(formingCandle(uidSBER, t2, "270.9"))

	ctx := context.Background()
	waitFor(t, "confirmed complete bar", func() bool {
		got, err := sqlite.CandleRepo{}.Range(ctx, db, uidSBER, model.Interval5m, t1, t1)
		return err == nil && len(got) == 1 && got[0].Complete
	})
	got, _ := sqlite.CandleRepo{}.Range(ctx, db, uidSBER, model.Interval5m, t1, t1)
	if got[0].Close.String() != "272" {
		t.Fatalf("confirmed bar close = %s, want the unary 272 (not the forming 270.3)", got[0].Close)
	}
}

func TestGapEventTriggersBackfill(t *testing.T) {
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	backfilled := make(chan struct{}, 4)
	b.setCandles(func(_ string, _, _ time.Time) (tinvestcli.CandlesResult, error) {
		backfilled <- struct{}{}
		return mustCandles(t, `{"instrument_uid":"`+uidSBER+`","interval":"5m","candles":[`+
			bar("2026-07-19T09:10:00Z", "271", "272", "270", "271.5", "900", true)+`]}`), nil
	})
	db, s := tempStore(t)
	c, _ := market.New(deps(b, s, clock.Real()), market.Config{Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	h := <-b.handles
	<-backfilled // drain the startup backfill call

	h.feed(tinvestcli.GapEvent{InstrumentUID: uidSBER, From: time.Date(2026, 7, 19, 9, 10, 0, 0, time.UTC), Reason: "test gap"})

	ctx := context.Background()
	waitFor(t, "gap backfill row", func() bool {
		got, err := sqlite.CandleRepo{}.Range(ctx, db, uidSBER, model.Interval5m,
			time.Date(2026, 7, 19, 9, 10, 0, 0, time.UTC), time.Date(2026, 7, 19, 9, 10, 0, 0, time.UTC))
		return err == nil && len(got) == 1
	})
}

func TestQuoteFreshnessAndHealth(t *testing.T) {
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	now := time.Date(2026, 7, 19, 10, 0, 30, 0, time.UTC)
	clk := clock.NewSimulated(now)
	_, s := tempStore(t)
	c, _ := market.New(deps(b, s, clk), market.Config{Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	h := <-b.handles

	quoteTime := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC) // 30s before "now"
	h.feed(tinvestcli.LastPriceEvent{InstrumentUID: uidSBER, Price: model.MustDecimal("270.8"), Time: quoteTime})

	waitFor(t, "quote in health", func() bool {
		ih, ok := c.Health().Instruments[uidSBER]
		return ok && !ih.LastQuote.IsZero()
	})
	h2 := c.Health()
	if !h2.StreamUp {
		t.Fatal("stream should be up")
	}
	ih := h2.Instruments[uidSBER]
	if ih.Ticker != "SBER" {
		t.Fatalf("ticker = %q", ih.Ticker)
	}
	if ih.QuoteAge != 30*time.Second {
		t.Fatalf("QuoteAge = %v, want 30s", ih.QuoteAge)
	}
}

func TestStreamDownAuthIsTerminal(t *testing.T) {
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	_, s := tempStore(t)
	c, _ := market.New(deps(b, s, clock.Real()), market.Config{Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	h := <-b.handles

	// A terminal auth failure: the collector must mark down and NOT restart.
	h.feed(&tinvestcli.StreamDownError{Err: &tinvestcli.AuthError{BrokerError: tinvestcli.BrokerError{Code: "AUTH", Message: "token rejected"}}})
	h.end()

	waitFor(t, "stream marked down", func() bool { return !c.Health().StreamUp })
	// No new stream connection should be attempted.
	select {
	case <-b.handles:
		t.Fatal("auth failure must not trigger a restart")
	case <-time.After(150 * time.Millisecond):
	}
	if got := c.Health().StreamRestarts; got != 0 {
		t.Fatalf("StreamRestarts = %d, want 0 (no restart on auth)", got)
	}
}

func TestStreamDownNetworkRestarts(t *testing.T) {
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	_, s := tempStore(t)
	c, _ := market.New(deps(b, s, clock.Real()), market.Config{
		Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m, StreamRestartBackoff: time.Millisecond,
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	h1 := <-b.handles
	h1.feed(&tinvestcli.StreamDownError{Err: &tinvestcli.NetworkError{BrokerError: tinvestcli.BrokerError{Code: "NETWORK", Message: "reset"}}})
	h1.end()

	// A transient network down must reconnect: a second stream connection appears.
	select {
	case <-b.handles:
	case <-time.After(3 * time.Second):
		t.Fatal("network stream-down did not restart")
	}
	waitFor(t, "restart counted", func() bool { return c.Health().StreamRestarts >= 1 })
}

func TestCleanShutdownNoLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	b := newFakeBroker()
	b.addInstrument("SBER@TQBR", uidSBER, "SBER")
	_, s := tempStore(t)
	c, _ := market.New(deps(b, s, clock.Real()), market.Config{Universe: []string{"SBER@TQBR"}, Interval: model.Interval5m})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := <-b.handles
	h.feed(tinvestcli.LastPriceEvent{InstrumentUID: uidSBER, Price: model.MustDecimal("270.8"), Time: time.Now().UTC()})

	c.Stop() // must return promptly with the supervisor reaped
	for i := 0; i < 50; i++ {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: baseline %d, now %d", before, runtime.NumGoroutine())
}

// formingCandle builds an incomplete streamed candle event.
func formingCandle(uid string, ts time.Time, closePx string) tinvestcli.CandleEvent {
	return tinvestcli.CandleEvent{
		InstrumentUID: uid,
		Interval:      "SUBSCRIPTION_INTERVAL_FIVE_MINUTES",
		Open:          model.MustDecimal("270"),
		High:          model.MustDecimal("271"),
		Low:           model.MustDecimal("269"),
		Close:         model.MustDecimal(closePx),
		Volume:        100,
		CandleTime:    ts,
	}
}
