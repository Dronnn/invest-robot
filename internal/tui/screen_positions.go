package tui

import (
	"strings"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// positionsScreen is screen 2 (DESIGN §9): a table of held positions on the
// left, the selected position's fills history on the right.
type positionsScreen struct {
	rm   *readModel
	st   *styles
	clk  clock.Clock
	keys keyMap

	data     positionsData
	selected int
	loaded   bool

	fillsFor model.InstrumentUID
	fills    []fillRow
	fillsErr error
}

func newPositionsScreen(rm *readModel, st *styles, clk clock.Clock, keys keyMap) *positionsScreen {
	return &positionsScreen{rm: rm, st: st, clk: clk, keys: keys}
}

func (s *positionsScreen) Init() tea.Cmd      { return positionsCmd(s.rm) }
func (s *positionsScreen) refresh() tea.Cmd   { return positionsCmd(s.rm) }
func (s *positionsScreen) Title() string      { return "Positions" }
func (s *positionsScreen) StatusHint() string { return s.data.warn }

func (s *positionsScreen) ScreenHelp() []key.Binding {
	return []key.Binding{s.keys.Up, s.keys.Down}
}

func (s *positionsScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case positionsMsg:
		s.data = m.data
		s.loaded = true
		if s.selected >= len(s.data.rows) {
			s.selected = clampMin(len(s.data.rows)-1, 0)
		}
		return s, s.loadFills()
	case positionFillsMsg:
		if m.uid == s.selectedUID() {
			s.fills = m.rows
			s.fillsErr = m.err
			s.fillsFor = m.uid
		}
		return s, nil
	case tea.KeyMsg:
		switch {
		case key.Matches(m, s.keys.Down):
			return s, s.move(1)
		case key.Matches(m, s.keys.Up):
			return s, s.move(-1)
		}
	}
	return s, nil
}

func (s *positionsScreen) move(delta int) tea.Cmd {
	if len(s.data.rows) == 0 {
		return nil
	}
	s.selected += delta
	if s.selected < 0 {
		s.selected = 0
	}
	if s.selected >= len(s.data.rows) {
		s.selected = len(s.data.rows) - 1
	}
	return s.loadFills()
}

func (s *positionsScreen) selectedUID() model.InstrumentUID {
	if s.selected < 0 || s.selected >= len(s.data.rows) {
		return ""
	}
	return s.data.rows[s.selected].uid
}

// loadFills issues a fills fetch for the selected position if it differs from
// the one already loaded.
func (s *positionsScreen) loadFills() tea.Cmd {
	uid := s.selectedUID()
	if uid == "" || uid == s.fillsFor {
		return nil
	}
	s.fills = nil
	s.fillsErr = nil
	s.fillsFor = uid
	return positionFillsCmd(s.rm, uid)
}

func (s *positionsScreen) View(width, height int) string {
	left := s.listBody()
	right := s.detailBody()
	return s.st.twoPane("Positions", left, "Fills", right, width, height, true)
}

func (s *positionsScreen) listBody() string {
	if !s.loaded {
		return s.st.muted.Render("loading…")
	}
	if s.data.err != nil {
		return s.st.bad.Render("error: " + truncate(s.data.err.Error(), 40))
	}
	rows := make([]string, len(s.data.rows))
	for i, r := range s.data.rows {
		last := dash
		if r.priced {
			last = formatDecimal(r.lastPrice)
		}
		rows[i] = padRight(truncate(displayName(r.ticker, r.uid), 9), 10) +
			padRight(formatQty(r.qty), 7) + last
	}
	return s.st.renderRows(rows, s.selected, minInnerWidth, 100)
}

func (s *positionsScreen) detailBody() string {
	if !s.loaded || len(s.data.rows) == 0 {
		return s.st.muted.Render("no positions")
	}
	r := s.data.rows[s.selected]
	var b strings.Builder
	b.WriteString(s.st.heading.Render(displayName(r.ticker, r.uid)))
	b.WriteByte('\n')
	b.WriteString(s.st.field("Qty (lots)", s.st.value.Render(formatQty(r.qty))))
	b.WriteByte('\n')
	b.WriteString(s.st.field("Avg price", s.st.value.Render(formatDecimal(r.avgPrice))))
	b.WriteByte('\n')
	if r.priced {
		b.WriteString(s.st.field("Last", s.st.value.Render(formatDecimal(r.lastPrice))))
		b.WriteByte('\n')
		b.WriteString(s.st.field("Unrealized", s.st.signStyle(r.pnl).Render(formatSigned(r.pnl))))
	} else {
		b.WriteString(s.st.field("Last", s.st.muted.Render(dash+" (unpriced)")))
	}
	b.WriteString("\n\n")
	b.WriteString(s.st.label.Render("Fills history"))
	b.WriteByte('\n')
	b.WriteString(s.fillsBody())
	return b.String()
}

func (s *positionsScreen) fillsBody() string {
	if s.fillsErr != nil {
		return s.st.bad.Render("error: " + truncate(s.fillsErr.Error(), 40))
	}
	if len(s.fills) == 0 {
		return s.st.muted.Render("(no fills)")
	}
	var lines []string
	lines = append(lines, s.st.label.Render(
		padRight("time", 21)+padRight("qty", 6)+padRight("price", 12)+"fee"))
	for _, f := range s.fills {
		lines = append(lines, padRight(dateTime(f.ts), 21)+
			padRight(formatQty(f.qty), 6)+
			padRight(formatDecimal(f.price), 12)+formatDecimal(f.fee))
	}
	return strings.Join(lines, "\n")
}
