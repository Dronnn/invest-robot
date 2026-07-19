package tui

import (
	"sync"
	"time"
)

// EngineController is the TUI's read/command view of the autonomous engine. It
// is consumer-owned (defined here, in the TUI, not by the engine) and kept
// deliberately small and stable: the real cycle package (Step 12) implements
// it, and the TUI drives pause/resume and the kill switch through it without
// depending on that package.
//
// Every method must be safe to call from the UI goroutine and cheap — Status
// in particular is read on every render/tick, so implementations return an
// in-memory snapshot rather than touching I/O.
type EngineController interface {
	// Status returns the current engine snapshot.
	Status() EngineStatus
	// Pause halts new decision cycles; in-flight work is allowed to finish.
	Pause()
	// Resume lifts a pause.
	Resume()
	// KillSwitch engages flatten-only mode (DESIGN §9/§10): no new risk is
	// opened, and the engine works down to flat. It is intentionally one-way —
	// re-enabling trading is an out-of-band operator action, not a TUI key.
	KillSwitch()
}

// EngineState enumerates the values EngineStatus.State takes. They are plain
// strings so the interface stays free of engine internals.
const (
	EngineRunning = "running"
	EnginePaused  = "paused"
	EngineHalted  = "halted"
)

// EngineStatus is an in-memory snapshot of the engine for the TUI. Mode is the
// trading mode (PAPER/BACKTEST/REAL) as it should appear in the badge.
type EngineStatus struct {
	State       string // one of EngineRunning / EnginePaused / EngineHalted
	Mode        string // PAPER | BACKTEST | REAL
	NextCycleAt time.Time
	LastCycle   CycleSummary
}

// CycleSummary is a compact description of the most recent decision cycle,
// surfaced on the dashboard. It is a projection the engine fills in — the TUI
// renders it read-only. A zero value (ID == 0) means "no cycle has run yet".
type CycleSummary struct {
	ID        int64
	StartedAt time.Time
	Mode      string
	Engine    string
	Status    string // running | ok | rejected | error (engine-defined)
	Decisions int    // number of decisions emitted
	Orders    int    // number of resulting order intents
}

// CancelRequester lets the Orders screen ask the engine/executor to cancel an
// open order intent by its client order id. It is consumer-owned like
// EngineController; execution/cycle wires the concrete implementation later.
// The call only requests a cancel — the actual state change happens
// asynchronously in the executor and shows up on the next data refresh.
type CancelRequester interface {
	RequestCancel(clientOrderID string) error
}

// StubController is a fixed-value EngineController for tests and dev wiring. It
// records how often each command was invoked and reflects Pause/Resume/
// KillSwitch in the State it reports, so both the Update tests (which assert
// the TUI routed a key to the controller) and manual dev runs behave sensibly.
// It is safe for concurrent use.
type StubController struct {
	mu      sync.Mutex
	status  EngineStatus
	Pauses  int
	Resumes int
	Kills   int
}

// NewStubController returns a StubController reporting a running engine in the
// given mode (empty defaults to PAPER). Callers may set fields on the returned
// value before use via SetStatus.
func NewStubController(mode string) *StubController {
	if mode == "" {
		mode = ModePaper
	}
	return &StubController{status: EngineStatus{State: EngineRunning, Mode: mode}}
}

// SetStatus replaces the reported status (e.g. to give dev runs a countdown or
// a last-cycle summary). State is left as-is if the supplied State is empty.
func (c *StubController) SetStatus(s EngineStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.State == "" {
		s.State = c.status.State
	}
	c.status = s
}

// Status implements EngineController.
func (c *StubController) Status() EngineStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Pause implements EngineController.
func (c *StubController) Pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Pauses++
	c.status.State = EnginePaused
}

// Resume implements EngineController.
func (c *StubController) Resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Resumes++
	c.status.State = EngineRunning
}

// KillSwitch implements EngineController.
func (c *StubController) KillSwitch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Kills++
	c.status.State = EngineHalted
}

// StubCancelRequester records cancel requests and returns Err (nil by default).
// Safe for concurrent use.
type StubCancelRequester struct {
	mu        sync.Mutex
	Requested []string
	Err       error
}

// RequestCancel implements CancelRequester.
func (c *StubCancelRequester) RequestCancel(clientOrderID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Requested = append(c.Requested, clientOrderID)
	return c.Err
}

// Requests returns a copy of the recorded cancel requests.
func (c *StubCancelRequester) Requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.Requested))
	copy(out, c.Requested)
	return out
}
