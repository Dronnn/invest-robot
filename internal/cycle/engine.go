package cycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/decision"
	"github.com/Dronnn/invest-robot/internal/execution"
	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/portfolio"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// Engine states reported by Status. They mirror the TUI's string enum without
// importing it.
const (
	StateRunning = "running"
	StatePaused  = "paused"
	StateHalted  = "halted"
)

// Config configures the cycle engine. The app maps its config sections onto it.
type Config struct {
	Mode          string
	Interval      model.CandleInterval
	Currency      string
	FeatureParams features.Params
	Risk          config.RiskConfig
	Paper         config.PaperConfig

	// MaxDataAge is the freshness ceiling: a cycle with no instrument fresher
	// than this is skipped and recorded.
	MaxDataAge time.Duration
	// DecisionBudget bounds the engine's Decide via context. Default 30s.
	DecisionBudget time.Duration
	// LookbackBars is how many bars of history to load per instrument for
	// feature computation. Default 400.
	LookbackBars int

	// Session is the trading window in wall-clock "HH:MM" within Location; empty
	// bounds mean 24h trading. Location defaults to UTC.
	SessionStart string
	SessionEnd   string
	Location     *time.Location

	// ConfigSnapshot is the JSON config snapshot stored on every cycle row for
	// replay (DESIGN §5).
	ConfigSnapshot string
	// EngineVersion is stored per cycle; defaults to the engine's Version().
	EngineVersion string
}

// Deps are the engine's injected collaborators.
type Deps struct {
	DB        *sqlite.DB
	Clock     clock.Clock
	Engine    decision.Engine
	Executor  execution.Executor
	Portfolio *portfolio.Portfolio
}

// Summary is a compact description of the most recent cycle, surfaced to the
// TUI (adapted to tui.CycleSummary at the wiring layer).
type Summary struct {
	ID        int64
	StartedAt time.Time
	Mode      string
	Engine    string
	Status    string // running | ok | rejected | skipped | error
	Decisions int
	Orders    int
}

// Status is an in-memory snapshot of the engine for the TUI.
type Status struct {
	State       string
	Mode        string
	NextCycleAt time.Time
	LastCycle   Summary
}

// Engine runs the autonomous loop and exposes the operator controls.
type Engine struct {
	deps Deps
	cfg  Config

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu          sync.Mutex
	paused      bool
	nextCycleAt time.Time
	last        Summary
	ordersToday int
	ordersDay   time.Time // the UTC date ordersToday counts
}

// New validates deps/cfg and returns an Engine.
func New(deps Deps, cfg Config) (*Engine, error) {
	switch {
	case deps.DB == nil:
		return nil, errors.New("cycle: nil DB")
	case deps.Clock == nil:
		return nil, errors.New("cycle: nil Clock")
	case deps.Engine == nil:
		return nil, errors.New("cycle: nil decision Engine")
	case deps.Executor == nil:
		return nil, errors.New("cycle: nil Executor")
	case deps.Portfolio == nil:
		return nil, errors.New("cycle: nil Portfolio")
	}
	if !cfg.Interval.Valid() {
		return nil, fmt.Errorf("cycle: invalid interval %q", cfg.Interval)
	}
	if cfg.Mode == "" {
		cfg.Mode = "paper"
	}
	if cfg.DecisionBudget <= 0 {
		cfg.DecisionBudget = 30 * time.Second
	}
	if cfg.LookbackBars <= 0 {
		cfg.LookbackBars = 400
	}
	if cfg.MaxDataAge <= 0 {
		cfg.MaxDataAge = 30 * time.Minute
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.EngineVersion == "" {
		cfg.EngineVersion = deps.Engine.Version()
	}
	return &Engine{deps: deps, cfg: cfg}, nil
}

// Start launches the scheduler goroutine. Call Stop to reap it.
func (e *Engine) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.wg.Add(1)
	go e.loop(runCtx)
}

// Stop halts the scheduler and waits for the current cycle (if any) to finish.
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
}

// loop is the scheduler: it waits for each completed-candle boundary, gates on
// pause and the trading session, runs one cycle at a time (no overlap: a single
// goroutine), and records an overrun when a cycle spills past the next boundary.
func (e *Engine) loop(ctx context.Context) {
	defer e.wg.Done()
	d := intervalDuration(e.cfg.Interval)
	for {
		next := nextBoundary(e.deps.Clock.Now(), d)
		e.setNextCycleAt(next)
		select {
		case <-ctx.Done():
			return
		case <-e.deps.Clock.After(next.Sub(e.deps.Clock.Now())):
		}
		if ctx.Err() != nil {
			return
		}
		now := e.deps.Clock.Now()
		if e.isPaused() {
			continue
		}
		sess := e.sessionWindow(now)
		if !sess.IsOpen(now) {
			e.onSessionClosed(ctx, now, sess)
			continue
		}
		start := now
		if _, err := e.RunOnce(ctx); err != nil {
			e.logEvent(ctx, "error", "cycle_failed", err.Error())
		}
		if e.deps.Clock.Now().After(nextBoundary(start, d)) {
			e.logEvent(ctx, "warn", "cycle_overrun",
				"cycle ran past the next boundary; next tick coalesces")
		}
	}
}

// onSessionClosed expires resting day orders at the session close.
func (e *Engine) onSessionClosed(ctx context.Context, now time.Time, sess execution.Session) {
	if ex, ok := e.deps.Executor.(interface {
		ExpireDay(context.Context, time.Time) error
	}); ok && !sess.End.IsZero() {
		if err := ex.ExpireDay(ctx, sess.End); err != nil {
			e.logEvent(ctx, "warn", "expire_day_failed", err.Error())
		}
	}
}

// Pause halts new cycles; an in-flight cycle finishes.
func (e *Engine) Pause() {
	e.mu.Lock()
	e.paused = true
	e.mu.Unlock()
}

// Resume lifts a pause.
func (e *Engine) Resume() {
	e.mu.Lock()
	e.paused = false
	e.mu.Unlock()
}

// KillSwitch latches the durable operational halt (flatten-only): risk strips
// every new buy until an operator clears it. It is intentionally one-way here.
func (e *Engine) KillSwitch() {
	ctx := context.Background()
	if err := (sqlite.HaltRepo{}).Engage(ctx, e.deps.DB, "operator kill switch engaged", e.deps.Clock.Now()); err != nil {
		e.logEvent(ctx, "error", "kill_switch_failed", err.Error())
	}
}

// Status returns the current engine snapshot. State reflects a latched halt
// first, then pause, then running.
func (e *Engine) Status() Status {
	e.mu.Lock()
	paused := e.paused
	next := e.nextCycleAt
	last := e.last
	e.mu.Unlock()

	state := StateRunning
	if h, err := (sqlite.HaltRepo{}).Status(context.Background(), e.deps.DB); err == nil && h.Engaged {
		state = StateHalted
	} else if paused {
		state = StatePaused
	}
	return Status{State: state, Mode: e.cfg.Mode, NextCycleAt: next, LastCycle: last}
}

// RequestCancel cancels a resting order intent by client order id. It records
// the operator reason on the intent row; the change shows on the next refresh.
func (e *Engine) RequestCancel(clientOrderID string) error {
	ctx := context.Background()
	in, err := (sqlite.IntentRepo{}).Get(ctx, e.deps.DB, clientOrderID)
	if err != nil {
		return err
	}
	if in.State.IsTerminal() {
		return fmt.Errorf("cycle: order %s already %s", clientOrderID, in.State)
	}
	if !model.CanTransition(in.State, model.IntentCanceled) {
		return fmt.Errorf("cycle: order %s in state %s cannot be canceled", clientOrderID, in.State)
	}
	return (sqlite.IntentRepo{}).UpdateStateWithReason(ctx, e.deps.DB, clientOrderID,
		in.State, model.IntentCanceled, e.deps.Clock.Now(), "operator cancel")
}

func (e *Engine) isPaused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paused
}

func (e *Engine) setNextCycleAt(t time.Time) {
	e.mu.Lock()
	e.nextCycleAt = t
	e.mu.Unlock()
}

func (e *Engine) setLast(s Summary) {
	e.mu.Lock()
	e.last = s
	e.mu.Unlock()
}

// bumpOrdersToday adds n to today's order count, resetting at a UTC date change.
func (e *Engine) bumpOrdersToday(now time.Time, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	day := now.UTC().Truncate(24 * time.Hour)
	if !day.Equal(e.ordersDay) {
		e.ordersDay = day
		e.ordersToday = 0
	}
	e.ordersToday += n
}

func (e *Engine) ordersTodayCount(now time.Time) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	day := now.UTC().Truncate(24 * time.Hour)
	if !day.Equal(e.ordersDay) {
		return 0
	}
	return e.ordersToday
}

// sessionWindow returns today's session window in absolute time, or the zero
// (24h-open) Session when no hours are configured.
func (e *Engine) sessionWindow(now time.Time) execution.Session {
	if e.cfg.SessionStart == "" || e.cfg.SessionEnd == "" {
		return execution.Session{}
	}
	loc := e.cfg.Location
	local := now.In(loc)
	start, err1 := parseWallClock(e.cfg.SessionStart, local, loc)
	end, err2 := parseWallClock(e.cfg.SessionEnd, local, loc)
	if err1 != nil || err2 != nil {
		return execution.Session{}
	}
	return execution.Session{Start: start.UTC(), End: end.UTC()}
}

// parseWallClock resolves an "HH:MM" wall-clock time on the same date as ref, in
// loc.
func parseWallClock(hhmm string, ref time.Time, loc *time.Location) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), 0, 0, loc), nil
}

func (e *Engine) logEvent(ctx context.Context, level, code, payload string) {
	_, _ = (sqlite.EventRepo{}).Insert(ctx, e.deps.DB, sqlite.Event{
		TS: e.deps.Clock.Now(), Level: level, Code: code, Payload: payload,
	})
}

// nextBoundary is the first interval boundary strictly after now.
func nextBoundary(now time.Time, interval time.Duration) time.Time {
	b := now.Truncate(interval)
	if !b.After(now) {
		b = b.Add(interval)
	}
	return b
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

// marshalJSON is json.Marshal returning "" on error, for best-effort persisted
// payloads.
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
