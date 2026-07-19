package tui

import (
	"strings"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// logScreen is screen 5 (DESIGN §9): the structured event log with a level
// filter. 'f' cycles the minimum level shown (all → error → warn → info).
type logScreen struct {
	rm   *readModel
	st   *styles
	keys keyMap
	clk  clock.Clock

	data     logData
	selected int
	loaded   bool
	minLevel int // index into levelFilters
}

// levelFilters are the cycle order for 'f': each is the minimum severity shown.
var levelFilters = []struct {
	name string
	min  int
}{
	{"all", 0},
	{"error", 3},
	{"warn", 2},
	{"info", 1},
}

// levelSeverity maps a level string to a comparable severity.
func levelSeverity(level string) int {
	switch strings.ToLower(level) {
	case "error", "fatal", "crit", "critical":
		return 3
	case "warn", "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func newLogScreen(rm *readModel, st *styles, clk clock.Clock, keys keyMap) *logScreen {
	return &logScreen{rm: rm, st: st, clk: clk, keys: keys}
}

func (s *logScreen) Init() tea.Cmd      { return logCmd(s.rm) }
func (s *logScreen) refresh() tea.Cmd   { return logCmd(s.rm) }
func (s *logScreen) Title() string      { return "Log" }
func (s *logScreen) StatusHint() string { return "filter: " + levelFilters[s.minLevel].name }

func (s *logScreen) ScreenHelp() []key.Binding {
	return []key.Binding{s.keys.Up, s.keys.Down, s.keys.Filter}
}

func (s *logScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case logMsg:
		s.data = m.data
		s.loaded = true
		s.clampSelection()
		return s, nil
	case tea.KeyMsg:
		switch {
		case key.Matches(m, s.keys.Down):
			s.move(1)
		case key.Matches(m, s.keys.Up):
			s.move(-1)
		case key.Matches(m, s.keys.Filter):
			s.minLevel = (s.minLevel + 1) % len(levelFilters)
			s.selected = 0
		}
	}
	return s, nil
}

// filtered returns the events passing the current level filter, most-recent
// first (as fetched).
func (s *logScreen) filtered() []eventRow {
	min := levelFilters[s.minLevel].min
	if min == 0 {
		return s.data.rows
	}
	out := make([]eventRow, 0, len(s.data.rows))
	for _, e := range s.data.rows {
		if levelSeverity(e.level) >= min {
			out = append(out, e)
		}
	}
	return out
}

func (s *logScreen) move(delta int) {
	n := len(s.filtered())
	if n == 0 {
		return
	}
	s.selected += delta
	if s.selected < 0 {
		s.selected = 0
	}
	if s.selected >= n {
		s.selected = n - 1
	}
}

func (s *logScreen) clampSelection() {
	n := len(s.filtered())
	if s.selected >= n {
		s.selected = clampMin(n-1, 0)
	}
}

func (s *logScreen) View(width, height int) string {
	return s.st.pane("Events ("+levelFilters[s.minLevel].name+")", s.body(width-2, height-3), width, height, true)
}

func (s *logScreen) body(innerW, innerH int) string {
	if !s.loaded {
		return s.st.muted.Render("loading…")
	}
	if s.data.err != nil {
		return s.st.bad.Render("error: " + truncate(s.data.err.Error(), innerW-8))
	}
	rows := s.filtered()
	if len(rows) == 0 {
		return s.st.muted.Render("(no events)")
	}
	start, end := listWindow(len(rows), innerH, s.selected)
	var b strings.Builder
	for i := start; i < end; i++ {
		e := rows[i]
		marker := "  "
		if i == s.selected {
			marker = "▸ "
		}
		payload := ""
		if e.payload != "" {
			payload = " " + oneLine(e.payload)
		}
		line := marker + clockTime(e.ts) + " " + padRight(strings.ToUpper(e.level), 6) + " " +
			e.code + payload
		line = truncate(line, innerW)
		styled := s.st.levelStyle(e.level).Render(line)
		if i == s.selected {
			styled = s.st.rowSelected.Width(innerW).Render(truncate(line, innerW))
		}
		b.WriteString(styled)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
