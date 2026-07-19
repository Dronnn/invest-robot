package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/market"
	"github.com/Dronnn/invest-robot/internal/portfolio"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
	"github.com/charmbracelet/bubbles/help"
	tea "github.com/charmbracelet/bubbletea"
)

// HealthProvider is the TUI's read-only view of market-data health
// (market.Collector satisfies it). It is optional in Deps; when absent, the
// dashboard reports stream health as unavailable rather than failing.
type HealthProvider interface {
	Health() market.Health
}

// Deps is everything the TUI needs to render. The store handle and portfolio
// are required; the rest have safe defaults so a minimal dev wiring works. The
// TUI only ever reads through these (DESIGN §3) — it never owns the loop.
type Deps struct {
	// DB is the store handle every repository query runs against (*sqlite.DB
	// or *sql.DB both satisfy sqlite.Querier). Required.
	DB sqlite.Querier
	// Portfolio provides Summary / DayPnL. Required.
	Portfolio *portfolio.Portfolio
	// Health provides stream/instrument freshness. Optional.
	Health HealthProvider
	// Controller drives pause/resume/kill and reports engine status. Optional;
	// defaults to a StubController in the given Mode.
	Controller EngineController
	// Canceller handles manual order cancels. Optional; defaults to a
	// StubCancelRequester.
	Canceller CancelRequester
	// Clock is the time source for countdowns/ages. Optional; defaults to real.
	Clock clock.Clock

	// Currency the account settles in (e.g. "rub"); used for cash balance and
	// money labels. Empty defaults to "rub".
	Currency string
	// Mode is the initial mode badge (PAPER/BACKTEST/REAL); the controller's
	// reported Mode takes over once wired. Empty defaults to PAPER.
	Mode string
	// SessionStart is the DayPnL baseline. Zero defaults to today's UTC midnight.
	SessionStart time.Time
	// RefreshInterval is the periodic data-refresh cadence. Zero defaults to 2s.
	RefreshInterval time.Duration
	// QueryTimeout bounds each data query. Zero defaults to 3s.
	QueryTimeout time.Duration
}

// App is a constructed, ready-to-run TUI.
type App struct {
	root *rootModel
	rm   *readModel
}

// New validates deps, applies defaults, and builds the composed model tree.
func New(d Deps) (*App, error) {
	if d.DB == nil {
		return nil, errors.New("tui: Deps.DB is required")
	}
	if d.Portfolio == nil {
		return nil, errors.New("tui: Deps.Portfolio is required")
	}

	clk := d.Clock
	if clk == nil {
		clk = clock.Real()
	}
	currency := d.Currency
	if currency == "" {
		currency = "rub"
	}
	mode := normalizeMode(d.Mode)
	controller := d.Controller
	if controller == nil {
		controller = NewStubController(mode)
	}
	canceller := d.Canceller
	if canceller == nil {
		canceller = &StubCancelRequester{}
	}
	sessionStart := d.SessionStart
	if sessionStart.IsZero() {
		sessionStart = clk.Now().Truncate(24 * time.Hour)
	}
	refresh := d.RefreshInterval
	if refresh <= 0 {
		refresh = defaultRefreshInterval
	}
	queryTimeout := d.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = defaultQueryTimeout
	}

	rm := &readModel{
		q:            d.DB,
		pf:           d.Portfolio,
		health:       d.Health,
		clk:          clk,
		currency:     currency,
		sessionStart: sessionStart,
		queryTimeout: queryTimeout,
	}

	st := newStyles()
	keys := newKeyMap()
	screens := []screen{
		newDashboardScreen(rm, st, clk, controller),
		newPositionsScreen(rm, st, clk, keys),
		newDecisionsScreen(rm, st, clk, keys),
		newOrdersScreen(rm, st, clk, keys, canceller),
		newLogScreen(rm, st, clk, keys),
	}

	root := &rootModel{
		screens:    screens,
		active:     screenDashboard,
		controller: controller,
		clk:        clk,
		st:         st,
		keys:       keys,
		help:       help.New(),
		refresh:    refresh,
	}

	return &App{root: root, rm: rm}, nil
}

// Run starts the Bubble Tea program bound to ctx: cancelling ctx (or the
// operator pressing q / Ctrl-C) tears the UI down cleanly. It blocks until the
// UI exits and returns nil on a normal shutdown.
func (a *App) Run(ctx context.Context) error {
	a.rm.baseCtx = ctx
	p := tea.NewProgram(a.root, tea.WithContext(ctx), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted) {
			return nil // ctx cancel / SIGINT are normal shutdowns
		}
		return fmt.Errorf("tui: run: %w", err)
	}
	return nil
}
