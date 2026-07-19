package tui

import (
	"sort"
	"strings"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/market"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// dashboardScreen is screen 1 (DESIGN §9): equity, cash, day P&L, open
// positions, last-cycle summary, next-cycle countdown and stream health. It
// reads engine status from the controller directly (cheap, in-memory) and the
// account/health figures from the read model.
type dashboardScreen struct {
	rm         *readModel
	st         *styles
	clk        clock.Clock
	controller EngineController

	data   dashboardData
	loaded bool
}

func newDashboardScreen(rm *readModel, st *styles, clk clock.Clock, c EngineController) *dashboardScreen {
	return &dashboardScreen{rm: rm, st: st, clk: clk, controller: c}
}

func (s *dashboardScreen) Init() tea.Cmd      { return dashboardCmd(s.rm) }
func (s *dashboardScreen) refresh() tea.Cmd   { return dashboardCmd(s.rm) }
func (s *dashboardScreen) Title() string      { return "Dashboard" }
func (s *dashboardScreen) StatusHint() string { return "" }

func (s *dashboardScreen) ScreenHelp() []key.Binding { return nil }

func (s *dashboardScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	if m, ok := msg.(dashboardMsg); ok {
		s.data = m.data
		s.loaded = true
	}
	return s, nil
}

func (s *dashboardScreen) View(width, height int) string {
	topH := clampMin(height/2, minInnerHeight+1)
	botH := clampMin(height-topH, minInnerHeight+1)
	leftW := width / 2
	rightW := width - leftW

	top := lipgloss.JoinHorizontal(lipgloss.Top,
		s.st.pane("Account", s.accountBody(), leftW, topH, false),
		s.st.pane("Engine", s.engineBody(), rightW, topH, false),
	)
	bottom := s.st.pane("Stream health", s.healthBody(width-2), width, botH, false)
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

func (s *dashboardScreen) accountBody() string {
	if !s.loaded {
		return s.st.muted.Render("loading…")
	}
	if s.data.err != nil {
		return s.st.bad.Render("error: " + truncate(s.data.err.Error(), 60))
	}
	var lines []string

	equity := s.st.value.Render(dash)
	if s.data.equityKnown {
		equity = s.st.value.Render(formatMoney(s.data.equity, s.rm.currency))
	}
	lines = append(lines, s.st.field("Equity", equity))
	lines = append(lines, s.st.field("Cash", formatMoney(s.data.cash, s.rm.currency)))

	if s.data.dayPnLKnown {
		total := s.st.signStyle(s.data.dayTotal).Render(formatSigned(s.data.dayTotal))
		detail := s.st.muted.Render("  (realized " + formatSigned(s.data.dayRealized) +
			", unrealized " + formatSigned(s.data.dayUnrealized) + ")")
		lines = append(lines, s.st.field("Day P&L", total)+detail)
	} else {
		lines = append(lines, s.st.field("Day P&L", s.st.muted.Render(dash)))
	}
	lines = append(lines, s.st.field("Positions", s.st.value.Render(itoa(int64(s.data.positionCount)))))

	if s.data.warn != "" {
		lines = append(lines, s.st.warn.Render("! "+truncate(s.data.warn, 58)))
	}
	return strings.Join(lines, "\n")
}

func (s *dashboardScreen) engineBody() string {
	status := s.controller.Status()
	now := s.clk.Now()

	var lines []string
	lines = append(lines, s.st.field("Mode", s.st.badge(status.Mode)))
	lines = append(lines, s.st.field("State", s.st.engineStateLabel(status.State)))
	lines = append(lines, s.st.field("Next cycle", s.st.value.Render(countdown(now, status.NextCycleAt))))

	lc := status.LastCycle
	if lc.ID == 0 {
		lines = append(lines, s.st.field("Last cycle", s.st.muted.Render("(none yet)")))
	} else {
		summary := "#" + itoa(lc.ID) + " " + lc.Engine + " " + lc.Status +
			" (" + itoa(int64(lc.Decisions)) + " dec, " + itoa(int64(lc.Orders)) + " ord)"
		lines = append(lines, s.st.field("Last cycle", s.st.value.Render(summary)))
		lines = append(lines, s.st.field("  started", s.st.muted.Render(dateTime(lc.StartedAt))))
	}
	return strings.Join(lines, "\n")
}

func (s *dashboardScreen) healthBody(innerW int) string {
	if !s.loaded {
		return s.st.muted.Render("loading…")
	}
	if !s.data.healthKnown {
		return s.st.muted.Render("stream health unavailable")
	}
	h := s.data.health
	now := s.clk.Now()

	var lines []string
	streamState := s.st.good.Render("up")
	if !h.StreamUp {
		reason := h.StreamDownReason
		if reason == "" {
			reason = "down"
		}
		streamState = s.st.bad.Render("down: " + reason)
	}
	head := s.st.field("Stream", streamState) +
		s.st.muted.Render("   restarts "+itoa(int64(h.StreamRestarts))+
			"   last event "+ago(now, h.LastStreamEvent))
	lines = append(lines, head)

	if len(h.Instruments) == 0 {
		lines = append(lines, s.st.muted.Render("no instruments tracked"))
		return strings.Join(lines, "\n")
	}

	// Stable order by ticker/uid.
	keys := make([]string, 0, len(h.Instruments))
	byKey := map[string]market.InstrumentHealth{}
	for uid, ih := range h.Instruments {
		k := ih.Ticker
		if k == "" {
			k = string(uid)
		}
		keys = append(keys, k)
		byKey[k] = ih
	}
	sort.Strings(keys)

	lines = append(lines, s.st.label.Render(
		padRight("instrument", 14)+padRight("candle", 12)+padRight("quote", 12)+"state"))
	for _, k := range keys {
		ih := byKey[k]
		state := s.st.good.Render("ok")
		if ih.Stale {
			state = s.st.warn.Render("stale")
		}
		row := padRight(truncate(k, 13), 14) +
			padRight(ago(now, ih.CandleWatermark), 12) +
			padRight(ago(now, ih.LastQuote), 12) + state
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

// padRight pads s with spaces to at least width runes (no truncation — callers
// truncate first where needed).
func padRight(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
