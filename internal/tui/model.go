package tui

import (
	"strings"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/charmbracelet/bubbles/help"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Screen indices. Construction order in newRootModel must match these so data
// messages route to the right screen.
const (
	screenDashboard = iota
	screenPositions
	screenDecisions
	screenOrders
	screenLog
	screenCount
)

// killPhrase is the exact text the operator must type to arm the kill switch
// (DESIGN §9/§10 — a typed confirmation, not a single key).
const killPhrase = "KILL"

// rootModel is the top-level Bubble Tea model. It owns the chrome (tab bar,
// status bar, mode badge), the global keybindings, the help overlay and the
// kill-switch confirm modal, and forwards everything else to the active screen.
type rootModel struct {
	screens    []screen
	active     int
	controller EngineController
	clk        clock.Clock
	st         *styles
	keys       keyMap
	help       help.Model

	refresh       time.Duration // periodic tick cadence
	width, height int

	showHelp       bool
	killConfirming bool
	killBuffer     string
	status         string // transient global note (status bar)
}

func (m *rootModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(m.refresh), m.screens[m.active].Init())
}

func (m *rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(tickCmd(m.refresh), m.screens[m.active].refresh())

	case tea.KeyMsg:
		return m.handleKey(msg)

	default:
		// Data messages route to their owning screen.
		if idx, ok := routeMsg(msg); ok {
			updated, cmd := m.screens[idx].Update(msg)
			m.screens[idx] = updated
			return m, cmd
		}
	}
	return m, nil
}

// handleKey processes a key: kill-switch confirm capture first, then help
// dismissal, then global bindings, else forward to the active screen.
func (m *rootModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.killConfirming {
		return m.handleKillConfirm(msg), nil
	}

	if m.showHelp {
		// Any key closes the help overlay.
		m.showHelp = false
		return m, nil
	}

	switch {
	case matchesQuit(msg):
		return m, tea.Quit
	case msg.String() == "?":
		m.showHelp = true
		return m, nil
	case msg.String() == "p":
		m.togglePause()
		return m, nil
	case msg.String() == "K":
		m.killConfirming = true
		m.killBuffer = ""
		m.status = ""
		return m, nil
	case msg.String() == "tab":
		return m, m.switchTo((m.active + 1) % screenCount)
	case msg.String() == "shift+tab":
		return m, m.switchTo((m.active - 1 + screenCount) % screenCount)
	case isScreenDigit(msg.String()):
		return m, m.switchTo(int(msg.String()[0] - '1'))
	}

	updated, cmd := m.screens[m.active].Update(msg)
	m.screens[m.active] = updated
	return m, cmd
}

func (m *rootModel) handleKillConfirm(msg tea.KeyMsg) tea.Model {
	switch msg.Type {
	case tea.KeyEnter:
		if m.killBuffer == killPhrase {
			m.controller.KillSwitch()
			m.status = "kill switch engaged — flatten-only mode"
		} else {
			m.status = "kill switch aborted: phrase mismatch"
		}
		m.killConfirming = false
		m.killBuffer = ""
	case tea.KeyEsc:
		m.killConfirming = false
		m.killBuffer = ""
		m.status = "kill switch aborted"
	case tea.KeyBackspace:
		if n := len(m.killBuffer); n > 0 {
			m.killBuffer = m.killBuffer[:n-1]
		}
	case tea.KeyRunes:
		m.killBuffer += string(msg.Runes)
	case tea.KeySpace:
		m.killBuffer += " "
	}
	return m
}

func (m *rootModel) togglePause() {
	if m.controller.Status().State == EnginePaused {
		m.controller.Resume()
	} else {
		m.controller.Pause()
	}
}

// switchTo changes the active screen and returns its refresh command so the
// newly-shown screen loads immediately.
func (m *rootModel) switchTo(idx int) tea.Cmd {
	if idx < 0 || idx >= screenCount {
		return nil
	}
	m.active = idx
	m.status = ""
	return m.screens[idx].refresh()
}

func (m *rootModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return "initializing…"
	}
	contentH := clampMin(m.height-2, minInnerHeight+2)

	var body string
	switch {
	case m.killConfirming:
		body = m.killView(m.width, contentH)
	case m.showHelp:
		body = m.helpView(m.width, contentH)
	default:
		body = m.screens[m.active].View(m.width, contentH)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.tabBar(m.width),
		body,
		m.statusBar(m.width),
	)
}

func (m *rootModel) tabBar(width int) string {
	var tabs []string
	for i, sc := range m.screens {
		label := itoa(int64(i+1)) + " " + sc.Title()
		if i == m.active {
			tabs = append(tabs, m.st.tabActive.Render(label))
		} else {
			tabs = append(tabs, m.st.tabInactive.Render(label))
		}
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	return m.st.tabBar.MaxWidth(width).Render(bar)
}

func (m *rootModel) statusBar(width int) string {
	status := m.controller.Status()
	now := m.clk.Now()

	left := m.st.badge(status.Mode) + " " +
		m.st.engineStateLabel(status.State) +
		m.st.muted.Render("  next "+countdown(now, status.NextCycleAt))

	if hint := m.screens[m.active].StatusHint(); hint != "" {
		left += m.st.muted.Render("  · ") + m.st.value.Render(hint)
	}
	if m.status != "" {
		left += m.st.muted.Render("  · ") + m.st.warn.Render(m.status)
	}

	right := m.st.statusKey.Render("?") + m.st.statusBar.Render(" help  ") +
		m.st.statusKey.Render("q") + m.st.statusBar.Render(" quit")

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Too narrow for both; keep the left (mode + state) which is the
		// safety-critical part, clipped to width.
		return m.st.statusBar.MaxWidth(width).Render(left)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *rootModel) helpView(width, height int) string {
	body := m.st.overlayTitle.Render("Help") + "\n\n" +
		m.help.FullHelpView(m.keys.FullHelp())
	if sh := m.screens[m.active].ScreenHelp(); len(sh) > 0 {
		body += "\n\n" + m.st.label.Render(m.screens[m.active].Title()+" keys") +
			"\n" + m.help.ShortHelpView(sh)
	}
	body += "\n\n" + m.st.muted.Render("press any key to close")
	box := m.st.overlay.Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func (m *rootModel) killView(width, height int) string {
	typed := m.killBuffer
	if typed == "" {
		typed = m.st.muted.Render("(type here)")
	} else {
		typed = m.st.confirmInput.Render(typed)
	}
	body := m.st.badgeReal.Render(" KILL SWITCH ") + "\n\n" +
		"This engages flatten-only mode: no new risk is opened.\n\n" +
		"Type " + m.st.confirmInput.Render(killPhrase) + " and press Enter to confirm,\n" +
		"or Esc to abort.\n\n" +
		m.st.label.Render("> ") + typed
	box := m.st.overlay.Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// ---------------------------------------------------------------------------
// Key / message routing helpers
// ---------------------------------------------------------------------------

func matchesQuit(msg tea.KeyMsg) bool {
	return msg.String() == "q" || msg.Type == tea.KeyCtrlC
}

func isScreenDigit(s string) bool {
	return len(s) == 1 && s[0] >= '1' && s[0] <= byte('0'+screenCount)
}

// routeMsg maps a data message to its owning screen index.
func routeMsg(msg tea.Msg) (int, bool) {
	switch msg.(type) {
	case dashboardMsg:
		return screenDashboard, true
	case positionsMsg, positionFillsMsg:
		return screenPositions, true
	case cyclesMsg, cycleDetailMsg:
		return screenDecisions, true
	case ordersMsg, cancelResultMsg:
		return screenOrders, true
	case logMsg:
		return screenLog, true
	default:
		return 0, false
	}
}
