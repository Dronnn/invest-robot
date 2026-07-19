package market

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
)

// Config configures the collector. It is owned here; the app maps its config
// sections onto it (Universe from [universe], Interval from [schedule]).
type Config struct {
	// Universe is the configured instrument entries (tickers or UIDs), resolved
	// at Start.
	Universe []string
	// Interval is the candle interval collected and the cadence decisions align
	// to. Must be a supported model interval.
	Interval model.CandleInterval
	// LookbackBars is how many bars back to backfill when an instrument has no
	// stored watermark (feature warm-up depth). Default 300.
	LookbackBars int
	// StreamRestartBackoff is the cool-down before the collector reconnects a
	// stream that ended non-terminally. Default 5s.
	StreamRestartBackoff time.Duration
	// OrderbookDepth is the order-book subscription depth; the collector only
	// reads top-of-book, so any valid depth (1, 10, 20, 30, 40, 50) works.
	// Default 1. Zero or negative disables order-book subscription (quotes then
	// fall back to last price only).
	OrderbookDepth int
}

// Deps are the collector's injected collaborators.
type Deps struct {
	Broker      Broker
	Instruments InstrumentSink
	Candles     CandleStore
	Quotes      QuoteSink
	Events      EventLog
	Clock       clock.Clock
}

const (
	defaultLookbackBars   = 300
	defaultRestartBackoff = 5 * time.Second
	defaultOrderbookDepth = 1
)

// Collector resolves the universe, backfills authoritative candles, and
// supervises a marketdata stream for freshness. It is safe for concurrent use;
// Start launches the stream supervisor and Stop reaps it.
type Collector struct {
	broker      Broker
	instruments InstrumentSink
	candles     CandleStore
	quotes      QuoteSink
	events      EventLog
	clock       clock.Clock

	cfg      Config
	interval model.CandleInterval

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu               sync.Mutex
	universe         []resolved
	streamUp         bool
	streamRestarts   int
	streamDownReason string
	lastStreamEvent  time.Time
	instHealth       map[model.InstrumentUID]*instHealth
	forming          map[model.InstrumentUID]time.Time
}

// resolved is one resolved universe entry.
type resolved struct {
	uid    model.InstrumentUID
	ticker string
	entry  string
}

// New validates deps/cfg and returns a Collector. It does not touch the network;
// call Start for that.
func New(deps Deps, cfg Config) (*Collector, error) {
	switch {
	case deps.Broker == nil:
		return nil, errors.New("market: nil Broker")
	case deps.Instruments == nil || deps.Candles == nil || deps.Quotes == nil || deps.Events == nil:
		return nil, errors.New("market: nil store dependency")
	case deps.Clock == nil:
		return nil, errors.New("market: nil Clock")
	}
	if !cfg.Interval.Valid() {
		return nil, fmt.Errorf("market: invalid candle interval %q", cfg.Interval)
	}
	if len(cfg.Universe) == 0 {
		return nil, errors.New("market: empty universe")
	}
	if cfg.LookbackBars <= 0 {
		cfg.LookbackBars = defaultLookbackBars
	}
	if cfg.StreamRestartBackoff <= 0 {
		cfg.StreamRestartBackoff = defaultRestartBackoff
	}
	if cfg.OrderbookDepth == 0 {
		cfg.OrderbookDepth = defaultOrderbookDepth
	}
	return &Collector{
		broker:      deps.Broker,
		instruments: deps.Instruments,
		candles:     deps.Candles,
		quotes:      deps.Quotes,
		events:      deps.Events,
		clock:       deps.Clock,
		cfg:         cfg,
		interval:    cfg.Interval,
		instHealth:  map[model.InstrumentUID]*instHealth{},
		forming:     map[model.InstrumentUID]time.Time{},
	}, nil
}

// Start resolves the universe (a fail-closed step — an unknown entry aborts) and
// launches the stream supervisor. Backfill runs inside the supervisor so Start
// returns once the universe is known and persisted.
func (c *Collector) Start(ctx context.Context) error {
	if err := c.resolveUniverse(ctx); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.wg.Add(1)
	go c.superviseStream(runCtx)
	return nil
}

// Stop shuts the collector down and waits for the supervisor and its child to
// be reaped.
func (c *Collector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

// resolveUniverse resolves and persists every configured entry. Any entry that
// cannot be resolved makes Start fail closed, listing all offenders.
func (c *Collector) resolveUniverse(ctx context.Context) error {
	seen := map[model.InstrumentUID]bool{}
	var bad []string
	for _, entry := range c.cfg.Universe {
		inst, err := c.broker.InstrumentGet(ctx, entry)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%q (%v)", entry, err))
			continue
		}
		m := inst.Model()
		if seen[m.UID] {
			continue
		}
		if err := c.instruments.UpsertInstrument(ctx, m, c.clock.Now()); err != nil {
			return fmt.Errorf("market: persist instrument %s: %w", m.UID, err)
		}
		seen[m.UID] = true
		c.universe = append(c.universe, resolved{uid: m.UID, ticker: m.Ticker, entry: entry})
		c.instHealth[m.UID] = &instHealth{ticker: m.Ticker}
	}
	if len(bad) > 0 {
		return fmt.Errorf("market: unresolved universe entries: %s", strings.Join(bad, "; "))
	}
	if len(c.universe) == 0 {
		return errors.New("market: universe resolved to no instruments")
	}
	return nil
}

// superviseStream backfills the missed range, connects the stream, and consumes
// events until it ends; a non-terminal end reconnects after a backoff, a
// terminal (auth/usage/policy) end stops, and context cancellation shuts down.
func (c *Collector) superviseStream(ctx context.Context) {
	defer c.wg.Done()

	first := true
	for {
		if ctx.Err() != nil {
			return
		}

		reason := "startup"
		if !first {
			reason = "stream-restart"
		}
		first = false
		c.backfillAll(ctx, time.Time{}, reason)
		if ctx.Err() != nil {
			return
		}

		handle, err := c.broker.StreamMarketdata(ctx, c.streamRequest())
		if err != nil {
			c.setStreamDown(err.Error())
			c.logEvent(ctx, LevelError, "stream_start_failed", err.Error())
			if permanentStreamErr(err) {
				return
			}
			if !c.sleep(ctx, c.cfg.StreamRestartBackoff) {
				return
			}
			continue
		}

		c.setStreamUp()
		cause := c.consume(ctx, handle)
		_ = handle.Close()

		if ctx.Err() != nil {
			c.setStreamDown("shutdown")
			return
		}
		reasonStr := "stream ended"
		if cause != nil {
			reasonStr = cause.Error()
		}
		c.setStreamDown(reasonStr)
		c.logEvent(ctx, LevelWarn, "stream_down", reasonStr)
		if permanentStreamErr(cause) {
			return
		}
		c.incStreamRestart()
		if !c.sleep(ctx, c.cfg.StreamRestartBackoff) {
			return
		}
	}
}

// consume reads events until the stream channel closes or the context is
// canceled, returning the terminal StreamDownError cause if one was delivered.
func (c *Collector) consume(ctx context.Context, handle StreamHandle) error {
	var cause error
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-handle.Events():
			if !ok {
				return cause
			}
			c.noteStreamEvent()
			switch e := ev.(type) {
			case tinvestcli.CandleEvent:
				c.onCandle(ctx, e)
			case tinvestcli.LastPriceEvent:
				c.onLastPrice(ctx, e)
			case tinvestcli.OrderbookEvent:
				c.onOrderbook(ctx, e)
			case tinvestcli.StatusEvent:
				c.onStatus(ctx, e)
			case tinvestcli.GapEvent:
				c.onGap(ctx, e)
			case *tinvestcli.StreamDownError:
				cause = e
			}
		}
	}
}

// streamRequest subscribes to candles, last prices, and (when a positive depth
// is configured) the order book for the whole universe. Top-of-book quotes let
// paper fills use a real bid/ask rather than the low-fidelity last-price
// fallback.
func (c *Collector) streamRequest() tinvestcli.StreamRequest {
	ids := make([]string, len(c.universe))
	for i, r := range c.universe {
		ids[i] = string(r.uid)
	}
	req := tinvestcli.StreamRequest{
		Instruments:    ids,
		Candles:        true,
		CandleInterval: c.interval,
		LastPrice:      true,
	}
	if c.cfg.OrderbookDepth > 0 {
		req.OrderbookDepth = c.cfg.OrderbookDepth
	}
	return req
}

// onCandle ingests a forming bar (written incomplete) and, on rollover to a
// newer bar, confirms the just-closed bar authoritatively via a unary fetch.
func (c *Collector) onCandle(ctx context.Context, e tinvestcli.CandleEvent) {
	uid := model.InstrumentUID(e.InstrumentUID)

	c.mu.Lock()
	prev, seen := c.forming[uid]
	c.forming[uid] = e.CandleTime
	if h := c.instHealth[uid]; h != nil {
		h.lastCandleEvent = e.CandleTime
	}
	c.mu.Unlock()

	mc, err := e.Model()
	if err != nil {
		c.logEvent(ctx, LevelWarn, "candle_decode_failed", fmt.Sprintf("%s: %v", uid, err))
		c.markStale(uid)
		return
	}
	if err := c.candles.UpsertCandle(ctx, mc); err != nil {
		c.logEvent(ctx, LevelWarn, "candle_upsert_failed", fmt.Sprintf("%s: %v", uid, err))
		c.markStale(uid)
		return
	}
	c.markFresh(uid)

	if seen && e.CandleTime.After(prev) {
		c.confirmBar(ctx, uid, prev)
	}
}

// confirmBar re-fetches a single just-closed bar via unary CandlesGet and, when
// the authoritative complete row is present, upserts it (replacing the earlier
// forming row) and advances the watermark. Failures degrade, never crash.
//
// The fetch window is [barTime, barTime+interval): the real CLI requires
// from < to and rejects from == to as a usage error, so a single-instant
// window can never confirm a bar. The response echo is validated against the
// request (uid and interval) before any row is trusted.
func (c *Collector) confirmBar(ctx context.Context, uid model.InstrumentUID, barTime time.Time) {
	to := barTime.Add(intervalDuration(c.interval))
	res, err := c.broker.CandlesGet(ctx, string(uid), c.interval, barTime, to)
	if err != nil {
		c.logEvent(ctx, LevelWarn, "confirm_fetch_failed", fmt.Sprintf("%s %s: %v", uid, barTime.Format(time.RFC3339), err))
		c.markStale(uid)
		return
	}
	if res.InstrumentUID != string(uid) {
		c.logEvent(ctx, LevelWarn, "confirm_uid_mismatch", fmt.Sprintf("%s: got %q", uid, res.InstrumentUID))
		c.markStale(uid)
		return
	}
	if iv, err := model.ParseCandleInterval(res.Interval); err != nil || iv != c.interval {
		c.logEvent(ctx, LevelWarn, "confirm_interval_mismatch", fmt.Sprintf("%s: got %q, want %s", uid, res.Interval, c.interval))
		c.markStale(uid)
		return
	}
	candles, err := res.Model()
	if err != nil {
		c.logEvent(ctx, LevelWarn, "confirm_decode_failed", fmt.Sprintf("%s: %v", uid, err))
		return
	}
	for _, mc := range candles {
		if mc.TS.Equal(barTime) && mc.Complete {
			if err := c.candles.UpsertCandle(ctx, mc); err != nil {
				c.logEvent(ctx, LevelWarn, "confirm_upsert_failed", fmt.Sprintf("%s: %v", uid, err))
				c.markStale(uid)
				return
			}
			c.advanceWatermark(uid, mc.TS)
			c.markFresh(uid)
			return
		}
	}
}

// onLastPrice ingests a quote tick.
func (c *Collector) onLastPrice(ctx context.Context, e tinvestcli.LastPriceEvent) {
	uid := model.InstrumentUID(e.InstrumentUID)
	if err := c.quotes.InsertQuote(ctx, e.Quote()); err != nil {
		c.logEvent(ctx, LevelWarn, "quote_insert_failed", fmt.Sprintf("%s: %v", uid, err))
		c.markStale(uid)
		return
	}
	c.mu.Lock()
	if h := c.instHealth[uid]; h != nil {
		h.lastQuote = e.Time
		h.stale = false
	}
	c.mu.Unlock()
}

// onOrderbook ingests a top-of-book snapshot as a high-fidelity quote (best bid
// and ask). The paper executor prefers a quote with a real bid/ask and only
// falls back to last price when they are absent, so surfacing these raises fill
// fidelity. The stored quote carries its own fidelity signal (Quote.HasBidAsk).
func (c *Collector) onOrderbook(ctx context.Context, e tinvestcli.OrderbookEvent) {
	uid := model.InstrumentUID(e.InstrumentUID)
	if err := c.quotes.InsertQuote(ctx, e.Quote()); err != nil {
		c.logEvent(ctx, LevelWarn, "orderbook_insert_failed", fmt.Sprintf("%s: %v", uid, err))
		c.markStale(uid)
		return
	}
	c.mu.Lock()
	if h := c.instHealth[uid]; h != nil {
		h.lastQuote = e.Time
		h.stale = false
	}
	c.mu.Unlock()
}

// onStatus records lifecycle frames; an in-band (non-shutdown) disconnect is
// logged but is not a collector restart — the CLI reconnects internally.
func (c *Collector) onStatus(ctx context.Context, e tinvestcli.StatusEvent) {
	if e.Kind == tinvestcli.StatusDisconnected && e.Reason != "shutdown" && !e.Final {
		c.logEvent(ctx, LevelInfo, "stream_disconnected", e.Reason)
	}
	if e.Kind == tinvestcli.StatusError && e.Err != nil {
		c.logEvent(ctx, LevelWarn, "stream_error_frame", e.Err.String())
	}
}

// onGap backfills the missing range a GapEvent reports. A zero From falls back
// to the watermark (per instrument for a targeted gap, inside backfillAll for a
// whole-subscription one).
func (c *Collector) onGap(ctx context.Context, e tinvestcli.GapEvent) {
	if e.InstrumentUID != "" {
		uid := model.InstrumentUID(e.InstrumentUID)
		from := e.From
		if from.IsZero() {
			from = c.backfillStart(ctx, uid)
		}
		to := e.To
		if to.IsZero() {
			to = c.clock.Now()
		}
		c.backfillOne(ctx, uid, from, to, "gap")
		return
	}
	c.backfillAll(ctx, e.From, "gap")
}

// backfillAll backfills every instrument. A zero from uses each instrument's
// own watermark (or the lookback window when it has none).
func (c *Collector) backfillAll(ctx context.Context, from time.Time, reason string) {
	to := c.clock.Now()
	for _, r := range c.snapshotUniverse() {
		if ctx.Err() != nil {
			return
		}
		f := from
		if f.IsZero() {
			f = c.backfillStart(ctx, r.uid)
		}
		c.backfillOne(ctx, r.uid, f, to, reason)
	}
}

// backfillOne fetches [from, to] for one instrument and upserts every bar,
// advancing the watermark for each complete one. Failures degrade.
func (c *Collector) backfillOne(ctx context.Context, uid model.InstrumentUID, from, to time.Time, reason string) {
	res, err := c.broker.CandlesGet(ctx, string(uid), c.interval, from, to)
	if err != nil {
		c.logEvent(ctx, LevelWarn, "backfill_failed", fmt.Sprintf("%s (%s): %v", uid, reason, err))
		c.markStale(uid)
		return
	}
	candles, err := res.Model()
	if err != nil {
		c.logEvent(ctx, LevelWarn, "backfill_decode_failed", fmt.Sprintf("%s: %v", uid, err))
		return
	}
	for _, mc := range candles {
		if err := c.candles.UpsertCandle(ctx, mc); err != nil {
			c.logEvent(ctx, LevelWarn, "backfill_upsert_failed", fmt.Sprintf("%s: %v", uid, err))
			c.markStale(uid)
			return
		}
		if mc.Complete {
			c.advanceWatermark(uid, mc.TS)
		}
	}
	c.markFresh(uid)
}

// backfillStart is the from-time for a watermark-based backfill: the bar after
// the latest stored complete bar, or the lookback window when there is none.
func (c *Collector) backfillStart(ctx context.Context, uid model.InstrumentUID) time.Time {
	wm, ok, err := c.candles.LatestComplete(ctx, uid, c.interval)
	if err != nil {
		c.logEvent(ctx, LevelWarn, "watermark_read_failed", fmt.Sprintf("%s: %v", uid, err))
		ok = false
	}
	if ok {
		return wm.TS.Add(intervalDuration(c.interval))
	}
	return c.clock.Now().Add(-time.Duration(c.cfg.LookbackBars) * intervalDuration(c.interval))
}

// Health returns a point-in-time freshness/stream snapshot.
func (c *Collector) Health() Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	h := Health{
		StreamUp:         c.streamUp,
		StreamRestarts:   c.streamRestarts,
		StreamDownReason: c.streamDownReason,
		LastStreamEvent:  c.lastStreamEvent,
		Instruments:      make(map[model.InstrumentUID]InstrumentHealth, len(c.instHealth)),
	}
	for uid, ih := range c.instHealth {
		snap := InstrumentHealth{
			Ticker:          ih.ticker,
			CandleWatermark: ih.watermark,
			LastCandleEvent: ih.lastCandleEvent,
			LastQuote:       ih.lastQuote,
			Stale:           ih.stale,
		}
		if !ih.lastQuote.IsZero() {
			snap.QuoteAge = now.Sub(ih.lastQuote)
		}
		h.Instruments[uid] = snap
	}
	return h
}

// --- health/state mutation helpers (all mutex-guarded) ---

func (c *Collector) snapshotUniverse() []resolved {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]resolved, len(c.universe))
	copy(out, c.universe)
	return out
}

func (c *Collector) noteStreamEvent() {
	c.mu.Lock()
	c.lastStreamEvent = c.clock.Now()
	c.mu.Unlock()
}

func (c *Collector) setStreamUp() {
	c.mu.Lock()
	c.streamUp = true
	c.streamDownReason = ""
	c.mu.Unlock()
}

func (c *Collector) setStreamDown(reason string) {
	c.mu.Lock()
	c.streamUp = false
	c.streamDownReason = reason
	c.mu.Unlock()
}

func (c *Collector) incStreamRestart() {
	c.mu.Lock()
	c.streamRestarts++
	c.mu.Unlock()
}

func (c *Collector) advanceWatermark(uid model.InstrumentUID, ts time.Time) {
	c.mu.Lock()
	if h := c.instHealth[uid]; h != nil && ts.After(h.watermark) {
		h.watermark = ts
	}
	c.mu.Unlock()
}

func (c *Collector) markStale(uid model.InstrumentUID) {
	c.mu.Lock()
	if h := c.instHealth[uid]; h != nil {
		h.stale = true
	}
	c.mu.Unlock()
}

func (c *Collector) markFresh(uid model.InstrumentUID) {
	c.mu.Lock()
	if h := c.instHealth[uid]; h != nil {
		h.stale = false
	}
	c.mu.Unlock()
}

func (c *Collector) logEvent(ctx context.Context, level Level, code, payload string) {
	// Best-effort: a logging failure must not break collection.
	_ = c.events.Log(ctx, LogEvent{TS: c.clock.Now(), Level: level, Code: code, Payload: payload})
}

// sleep waits d or until ctx is canceled, returning false if canceled.
func (c *Collector) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-c.clock.After(d):
		return true
	}
}

// permanentStreamErr reports whether a stream-down cause is non-restartable, so
// the collector must not build a fresh stream (which would reset the inner
// supervisor's circuit breaker and restart-loop forever). A cause is permanent
// when:
//   - the inner breaker already tripped (too many fast restarts), or
//   - it is an auth/usage/policy failure (reconnecting fails identically), or
//   - it is a protocol/schema violation or a broker rejection (the request or
//     the binary is wrong, not the transport).
//
// A plain network/transport StreamDownError stays restartable.
func permanentStreamErr(err error) bool {
	if err == nil {
		return false
	}
	var down *tinvestcli.StreamDownError
	if errors.As(err, &down) && down.CircuitTripped {
		return true
	}
	var auth *tinvestcli.AuthError
	var usage *tinvestcli.UsageError
	var policy *tinvestcli.PolicyError
	var proto *tinvestcli.ProtocolError
	var rejected *tinvestcli.BrokerRejectedError
	return errors.As(err, &auth) || errors.As(err, &usage) || errors.As(err, &policy) ||
		errors.As(err, &proto) || errors.As(err, &rejected)
}

// intervalDuration is the wall-clock span of one bar at the given interval.
func intervalDuration(iv model.CandleInterval) time.Duration {
	switch iv {
	case model.Interval1m:
		return time.Minute
	case model.Interval5m:
		return 5 * time.Minute
	case model.Interval15m:
		return 15 * time.Minute
	case model.Interval1h:
		return time.Hour
	case model.Interval1d:
		return 24 * time.Hour
	default:
		return 5 * time.Minute
	}
}
