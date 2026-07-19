package tinvestcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	rand "math/rand/v2"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// StreamRequest describes a `tinvest stream marketdata` subscription. At least
// one instrument is required. The robot runs a single long-lived stream over the
// whole configured universe rather than one process per instrument (DESIGN §4).
type StreamRequest struct {
	Account        string
	Instruments    []string             // repeated --instrument (uid, FIGI, or TICKER@CLASSCODE)
	Candles        bool                 // subscribe to candles (--candles)
	CandleInterval model.CandleInterval // candle interval; defaults to 1m when Candles is set and this is empty
	LastPrice      bool                 // subscribe to last prices (--last-price)
	OrderbookDepth int                  // subscribe to the order book at this depth (--orderbook); 0 to skip
}

// Stream is a supervised, long-lived marketdata stream. Consume Events until the
// channel closes; a StreamDownError is the last event delivered when the
// supervisor gives up. Call Close (or cancel the context passed to
// StreamMarketdata) to shut it down; Close blocks until the child is reaped and
// the channel is closed.
type Stream struct {
	client    *Client
	events    chan Event
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once

	mu             sync.Mutex
	pendingPrices  map[model.InstrumentUID]*LastPriceEvent
	pendingGap     *GapEvent
	lastCandleTime time.Time
}

// Events is the delivery channel. It is closed when the stream ends.
func (s *Stream) Events() <-chan Event { return s.events }

// Done is closed once the supervisor has fully exited (child reaped, channel
// closed).
func (s *Stream) Done() <-chan struct{} { return s.done }

// Close shuts the stream down and blocks until teardown completes. It is
// idempotent and safe to call concurrently.
func (s *Stream) Close() error {
	s.closeOnce.Do(s.cancel)
	<-s.done
	return nil
}

// StreamMarketdata starts a supervised marketdata stream. It returns
// immediately; the child is spawned and supervised on a background goroutine.
func (c *Client) StreamMarketdata(ctx context.Context, req StreamRequest) (*Stream, error) {
	if len(req.Instruments) == 0 {
		return nil, &UsageError{BrokerError: BrokerError{
			Code:    "USAGE",
			Message: "stream marketdata requires at least one instrument",
		}}
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		client:        c,
		events:        make(chan Event, c.cfg.StreamQueueSize),
		cancel:        cancel,
		done:          make(chan struct{}),
		pendingPrices: map[model.InstrumentUID]*LastPriceEvent{},
	}
	go s.supervise(sctx, c.streamArgv(req))
	return s, nil
}

// streamArgv builds the stream argv. Unlike unary calls it carries no --timeout:
// the stream is long-lived and the CLI's per-call deadline would kill it. The
// optional-value flags --candles and --orderbook must use the =value form.
func (c *Client) streamArgv(req StreamRequest) []string {
	argv := []string{"stream", "marketdata"}
	for _, id := range req.Instruments {
		argv = append(argv, "--instrument", id)
	}
	if req.Candles {
		interval := req.CandleInterval
		if interval == "" {
			interval = model.Interval1m
		}
		argv = append(argv, "--candles="+interval.String())
	}
	if req.LastPrice {
		argv = append(argv, "--last-price")
	}
	if req.OrderbookDepth > 0 {
		argv = append(argv, fmt.Sprintf("--orderbook=%d", req.OrderbookDepth))
	}
	if req.Account != "" {
		argv = append(argv, "--account", req.Account)
	}
	if c.cfg.Profile != "" {
		argv = append(argv, "--profile", c.cfg.Profile)
	}
	argv = append(argv, "-o", "json")
	return argv
}

// supervise runs the child, restarting it on unexpected exit with jittered
// backoff and a circuit breaker, until shutdown is requested or a
// non-restartable outcome is reached. The child reconnects internally, so only
// process exit is supervised (DESIGN §4).
func (s *Stream) supervise(ctx context.Context, argv []string) {
	defer close(s.done)
	defer close(s.events)

	consecutiveFast := 0
	attempts := 0
	for {
		if ctx.Err() != nil {
			s.flushPending()
			return
		}

		start := time.Now()
		terminal, clean := s.runOnce(ctx, argv)
		dur := time.Since(start)

		switch {
		case clean:
			s.flushPending()
			return
		case terminal != nil:
			// Auth / usage / schema / protocol: never restart-loop (DESIGN §4).
			s.flushPending()
			s.sendTerminal(ctx, &StreamDownError{Err: terminal, Attempts: attempts})
			return
		}

		// Unexpected exit: candles after the last one may be missing, so emit a
		// gap for the collector to backfill, then restart.
		s.deliverGap(GapEvent{From: s.gapFrom(start), Reason: "stream exited unexpectedly"})
		attempts++
		if dur < s.client.cfg.StreamMinHealthyRun {
			consecutiveFast++
		} else {
			consecutiveFast = 0
		}
		if consecutiveFast >= s.client.cfg.StreamMaxFastRestarts {
			s.flushPending()
			s.sendTerminal(ctx, &StreamDownError{
				Err:            fmt.Errorf("stream restarted %d times without staying up", consecutiveFast),
				Attempts:       attempts,
				CircuitTripped: true,
			})
			return
		}
		if !s.sleepBackoff(ctx, consecutiveFast) {
			s.flushPending()
			return
		}
	}
}

// runOnce spawns and reads one child process to completion. It returns a
// non-nil terminal error for a non-restartable outcome, and clean=true when the
// exit was caused by our own shutdown request (context cancellation).
func (s *Stream) runOnce(ctx context.Context, argv []string) (terminal error, clean bool) {
	cmd := exec.Command(s.client.path, argv...)
	cmd.Env = s.client.cfg.Env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &InternalError{BrokerError: BrokerError{Message: "stream stdout pipe: " + err.Error()}}, false
	}
	cmd.Stderr = &cappedBuffer{limit: s.client.cfg.MaxStderr}

	if err := cmd.Start(); err != nil {
		// The binary was resolved at startup; a start failure now is a setup
		// problem, not something to restart-loop on.
		return &ResolveError{Path: s.client.path, Err: err}, false
	}

	procDone := make(chan struct{})
	go s.signalOnCancel(ctx, cmd, procDone)

	var lastErr *BrokerError
	terminal = s.readStream(stdout, &lastErr)
	if terminal != nil {
		// We stopped reading before EOF (a terminal frame). Kill the child so it
		// can't block writing to a now-unread pipe and hang cmd.Wait; we are not
		// restarting this outcome anyway.
		_ = cmd.Process.Kill()
	}

	waitErr := cmd.Wait()
	close(procDone)

	exit := 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exit = exitErr.ExitCode()
	}

	clean = ctx.Err() != nil
	if !clean && terminal == nil {
		terminal = exitTerminal(exit, lastErr)
	}
	return terminal, clean
}

// signalOnCancel implements the shutdown ladder: on context cancellation, send
// SIGTERM, wait KillGrace, then SIGKILL. It exits as soon as the process is done.
func (s *Stream) signalOnCancel(ctx context.Context, cmd *exec.Cmd, procDone <-chan struct{}) {
	select {
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-procDone:
		case <-time.After(s.client.cfg.KillGrace):
			_ = cmd.Process.Kill()
		}
	case <-procDone:
	}
}

// readStream reads the NDJSON stream line by line, bounding line length,
// checking schema_version per line, and dispatching typed events. It returns a
// terminal error for a protocol/schema/auth-class condition; a plain read error
// (the pipe closing on process exit) is the normal end and returns nil.
func (s *Stream) readStream(r io.Reader, lastErr **BrokerError) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), s.client.cfg.StreamLineLimit)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var f wireStreamFrame
		if err := json.Unmarshal(line, &f); err != nil {
			return &ProtocolError{Reason: "malformed stream frame", Err: err}
		}
		if !slices.Contains(s.client.cfg.SchemaVersions, f.SchemaVersion) {
			return &ProtocolError{
				Reason: "unknown stream schema_version",
				Detail: fmt.Sprintf("got %q, allow %v", f.SchemaVersion, s.client.cfg.SchemaVersions),
			}
		}
		if cause := s.dispatch(f, lastErr); cause != nil {
			return cause
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return &ProtocolError{Reason: "stream line exceeded the configured limit"}
		}
		// Any other read error is the pipe closing as the process exits — the
		// normal end of a run, handled by the exit-code classification.
	}
	return nil
}

// dispatch turns one frame into a typed event and delivers it, returning a
// terminal error for an auth/usage/policy error frame.
func (s *Stream) dispatch(f wireStreamFrame, lastErr **BrokerError) error {
	switch f.Type {
	case "candle":
		var wc wireStreamCandle
		if err := json.Unmarshal(f.Data, &wc); err != nil {
			return &ProtocolError{Reason: "malformed candle frame", Err: err}
		}
		ev := CandleEvent{
			InstrumentUID: wc.InstrumentUID,
			Ticker:        wc.Ticker,
			ClassCode:     wc.ClassCode,
			FIGI:          wc.FIGI,
			Interval:      wc.Interval,
			Open:          wc.Open.Amount,
			High:          wc.High.Amount,
			Low:           wc.Low.Amount,
			Close:         wc.Close.Amount,
			Volume:        wc.Volume.Int64(),
			VolumeBuy:     wc.VolumeBuy.Int64(),
			VolumeSell:    wc.VolumeSell.Int64(),
			CandleTime:    wc.CandleTime.UTC(),
			LastTradeTime: wc.LastTradeTime.UTC(),
			Source:        wc.Source,
		}
		s.deliverCandle(ev)

	case "last_price":
		var wp wireStreamLastPrice
		if err := json.Unmarshal(f.Data, &wp); err != nil {
			return &ProtocolError{Reason: "malformed last_price frame", Err: err}
		}
		s.deliverLastPrice(LastPriceEvent{
			InstrumentUID: wp.InstrumentUID,
			Ticker:        wp.Ticker,
			ClassCode:     wp.ClassCode,
			FIGI:          wp.FIGI,
			Price:         wp.Price.Amount,
			PriceType:     wp.PriceType,
			Time:          wp.Time.UTC(),
		})

	case "error":
		be := f.Error.broker()
		*lastErr = &be
		s.deliverStatus(StatusEvent{Kind: StatusError, Time: f.Time.UTC(), Err: &be})
		if cause := errForCode(be); cause != nil {
			return cause
		}

	default: // lifecycle: connected / disconnected / resubscribed / lagging / unknown
		var lc wireStreamLifecycle
		if len(f.Data) > 0 {
			_ = json.Unmarshal(f.Data, &lc) // best effort; lifecycle data is optional
		}
		s.deliverStatus(StatusEvent{
			Kind:          StatusKind(f.Type),
			Time:          f.Time.UTC(),
			Attempt:       lc.Attempt,
			Subscriptions: lc.Subscriptions,
			Reason:        lc.Reason,
			Final:         lc.Final,
		})
		// A non-shutdown disconnect means data may have been missed while the
		// feed was down: emit a gap so the collector backfills.
		if f.Type == string(StatusDisconnected) && lc.Reason != "shutdown" && !lc.Final {
			s.deliverGap(GapEvent{From: s.gapFrom(f.Time), Reason: "stream disconnected: " + lc.Reason})
		}
	}
	return nil
}

// exitTerminal maps a process exit code to a non-restartable cause, or nil when
// the exit is restartable. lastErr (the last error frame seen) refines exit 2.
func exitTerminal(exit int, lastErr *BrokerError) error {
	be := BrokerError{}
	if lastErr != nil {
		be = *lastErr
	}
	switch exit {
	case exitAuth:
		return &AuthError{BrokerError: be}
	case exitUsage:
		if be.Code == "POLICY" {
			return &PolicyError{BrokerError: be}
		}
		return &UsageError{BrokerError: be}
	default:
		return nil
	}
}

// errForCode maps an in-band error frame's code to a non-restartable cause, or
// nil when the frame is a transient error the supervisor may restart through.
func errForCode(be BrokerError) error {
	switch be.Code {
	case "AUTH":
		return &AuthError{BrokerError: be}
	case "USAGE":
		return &UsageError{BrokerError: be}
	case "POLICY":
		return &PolicyError{BrokerError: be}
	default:
		return nil
	}
}

// --- delivery: bounded queue, last-price coalescing, candles never lost ---

func (s *Stream) deliverCandle(ev CandleEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.CandleTime.After(s.lastCandleTime) {
		s.lastCandleTime = ev.CandleTime
	}
	s.flushPendingLocked()
	if !s.trySendLocked(ev) {
		// A candle is never silently dropped: record it as a gap to backfill.
		s.mergeGapLocked(ev.CandleTime, "candle dropped: queue full")
	}
}

func (s *Stream) deliverLastPrice(ev LastPriceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushPendingLocked()
	if !s.trySendLocked(ev) {
		// Last prices are replaceable: keep only the latest per instrument.
		e := ev
		s.pendingPrices[model.InstrumentUID(ev.InstrumentUID)] = &e
	}
}

func (s *Stream) deliverStatus(ev StatusEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushPendingLocked()
	_ = s.trySendLocked(ev) // informational; drop if the queue is full
}

func (s *Stream) deliverGap(ev GapEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mergeGapLocked(ev.From, ev.Reason)
	s.flushPendingLocked()
}

func (s *Stream) flushPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushPendingLocked()
}

// flushPendingLocked opportunistically drains the coalesced gap and last prices
// into the channel while there is room, preserving order (gap first).
func (s *Stream) flushPendingLocked() {
	if s.pendingGap != nil {
		if !s.trySendLocked(*s.pendingGap) {
			return
		}
		s.pendingGap = nil
	}
	for uid, p := range s.pendingPrices {
		if !s.trySendLocked(*p) {
			return
		}
		delete(s.pendingPrices, uid)
	}
}

func (s *Stream) trySendLocked(ev Event) bool {
	select {
	case s.events <- ev:
		return true
	default:
		return false
	}
}

func (s *Stream) mergeGapLocked(from time.Time, reason string) {
	if s.pendingGap == nil {
		s.pendingGap = &GapEvent{From: from, Reason: reason}
		return
	}
	if s.pendingGap.From.IsZero() || from.Before(s.pendingGap.From) {
		s.pendingGap.From = from
	}
}

// gapFrom returns the earliest point a gap should start from: the last observed
// candle time, or the given fallback when no candle has been seen.
func (s *Stream) gapFrom(fallback time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastCandleTime.IsZero() {
		return fallback.UTC()
	}
	return s.lastCandleTime
}

// sendTerminal delivers the final StreamDownError, giving up if shutdown is
// requested first so the goroutine never blocks on a stopped consumer.
func (s *Stream) sendTerminal(ctx context.Context, e *StreamDownError) {
	select {
	case s.events <- e:
	case <-ctx.Done():
	}
}

// sleepBackoff waits a jittered exponential backoff, returning false if the
// context is canceled (shutdown) during the wait.
func (s *Stream) sleepBackoff(ctx context.Context, n int) bool {
	d := s.backoffDuration(n)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// backoffDuration is base*2^(n-1) capped at max, with jitter in [d/2, d].
func (s *Stream) backoffDuration(n int) time.Duration {
	base := s.client.cfg.StreamBaseBackoff
	max := s.client.cfg.StreamMaxBackoff
	d := base
	for i := 1; i < n && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}
