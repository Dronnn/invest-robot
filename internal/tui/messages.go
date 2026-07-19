package tui

import (
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	tea "github.com/charmbracelet/bubbletea"
)

// Timing defaults; overridable via Deps.
const (
	defaultRefreshInterval = 2 * time.Second
	defaultQueryTimeout    = 3 * time.Second
)

// tickMsg is the periodic refresh signal (tea.Tick, DESIGN §9).
type tickMsg struct{ t time.Time }

// Data messages carry the shaped read-model payloads back to the UI goroutine.
// Each is tagged implicitly by its type so the root can route it to the owning
// screen.
type (
	dashboardMsg     struct{ data dashboardData }
	positionsMsg     struct{ data positionsData }
	positionFillsMsg struct {
		uid  model.InstrumentUID
		rows []fillRow
		err  error
	}
	cyclesMsg      struct{ data decisionsData }
	cycleDetailMsg struct{ detail cycleDetail }
	ordersMsg      struct{ data ordersData }
	logMsg         struct{ data logData }

	// cancelResultMsg reports the outcome of a manual cancel request.
	cancelResultMsg struct {
		clientOrderID string
		err           error
	}
)

// tickCmd arms the periodic refresh. It uses tea's own timer for the repaint
// cadence (the injected clock governs decision timing / replay, not UI paints).
func tickCmd(d time.Duration) tea.Cmd {
	if d <= 0 {
		d = defaultRefreshInterval
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg{t: t} })
}

func dashboardCmd(rm *readModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := rm.queryCtx()
		defer cancel()
		return dashboardMsg{data: rm.dashboard(ctx)}
	}
}

func positionsCmd(rm *readModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := rm.queryCtx()
		defer cancel()
		return positionsMsg{data: rm.positions(ctx)}
	}
}

func positionFillsCmd(rm *readModel, uid model.InstrumentUID) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := rm.queryCtx()
		defer cancel()
		rows, err := rm.fills(ctx, uid)
		return positionFillsMsg{uid: uid, rows: rows, err: err}
	}
}

func cyclesCmd(rm *readModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := rm.queryCtx()
		defer cancel()
		return cyclesMsg{data: rm.cycles(ctx)}
	}
}

func cycleDetailCmd(rm *readModel, cycleID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := rm.queryCtx()
		defer cancel()
		return cycleDetailMsg{detail: rm.cycleDetail(ctx, cycleID)}
	}
}

func ordersCmd(rm *readModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := rm.queryCtx()
		defer cancel()
		return ordersMsg{data: rm.orders(ctx)}
	}
}

func logCmd(rm *readModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := rm.queryCtx()
		defer cancel()
		return logMsg{data: rm.events(ctx)}
	}
}

// cancelCmd requests a cancel through the consumer-owned CancelRequester. The
// state change happens asynchronously in the executor; the next data refresh
// reflects it.
func cancelCmd(c CancelRequester, clientOrderID string) tea.Cmd {
	return func() tea.Msg {
		err := c.RequestCancel(clientOrderID)
		return cancelResultMsg{clientOrderID: clientOrderID, err: err}
	}
}
