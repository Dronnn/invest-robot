package tui

import (
	"strconv"
	"strings"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// decisionsScreen is screen 3 (DESIGN §9): the replay view. A list of recent
// cycles on the left; on the right, the selected cycle's decisions (action,
// sizing, rationale, validation status) and any raw engine calls.
type decisionsScreen struct {
	rm   *readModel
	st   *styles
	clk  clock.Clock
	keys keyMap

	data     decisionsData
	selected int
	loaded   bool

	detail    cycleDetail
	detailFor int64
	hasDetail bool
}

func newDecisionsScreen(rm *readModel, st *styles, clk clock.Clock, keys keyMap) *decisionsScreen {
	return &decisionsScreen{rm: rm, st: st, clk: clk, keys: keys}
}

func (s *decisionsScreen) Init() tea.Cmd      { return cyclesCmd(s.rm) }
func (s *decisionsScreen) refresh() tea.Cmd   { return cyclesCmd(s.rm) }
func (s *decisionsScreen) Title() string      { return "Decisions" }
func (s *decisionsScreen) StatusHint() string { return "" }

func (s *decisionsScreen) ScreenHelp() []key.Binding {
	return []key.Binding{s.keys.Up, s.keys.Down}
}

func (s *decisionsScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case cyclesMsg:
		s.data = m.data
		s.loaded = true
		if s.selected >= len(s.data.rows) {
			s.selected = clampMin(len(s.data.rows)-1, 0)
		}
		return s, s.loadDetail()
	case cycleDetailMsg:
		if m.detail.cycleID == s.selectedID() {
			s.detail = m.detail
			s.detailFor = m.detail.cycleID
			s.hasDetail = true
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

func (s *decisionsScreen) move(delta int) tea.Cmd {
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
	return s.loadDetail()
}

func (s *decisionsScreen) selectedID() int64 {
	if s.selected < 0 || s.selected >= len(s.data.rows) {
		return 0
	}
	return s.data.rows[s.selected].id
}

func (s *decisionsScreen) loadDetail() tea.Cmd {
	id := s.selectedID()
	if id == 0 || (s.hasDetail && id == s.detailFor) {
		return nil
	}
	s.hasDetail = false
	s.detailFor = id
	return cycleDetailCmd(s.rm, id)
}

func (s *decisionsScreen) View(width, height int) string {
	return s.st.twoPane("Cycles", s.listBody(), "Replay", s.detailBody(), width, height, true)
}

func (s *decisionsScreen) listBody() string {
	if !s.loaded {
		return s.st.muted.Render("loading…")
	}
	if s.data.err != nil {
		return s.st.bad.Render("error: " + truncate(s.data.err.Error(), 40))
	}
	rows := make([]string, len(s.data.rows))
	for i, r := range s.data.rows {
		rows[i] = padRight("#"+itoa(r.id), 7) +
			padRight(clockTime(r.startedAt), 10) +
			padRight(truncate(r.engine, 9), 10) + r.status
	}
	return s.st.renderRows(rows, s.selected, minInnerWidth, 100)
}

func (s *decisionsScreen) detailBody() string {
	if !s.loaded || len(s.data.rows) == 0 {
		return s.st.muted.Render("no cycles")
	}
	c := s.data.rows[s.selected]
	var b strings.Builder
	b.WriteString(s.st.heading.Render("Cycle #" + itoa(c.id)))
	b.WriteByte('\n')
	b.WriteString(s.st.field("Started", s.st.value.Render(dateTime(c.startedAt))))
	b.WriteByte('\n')
	b.WriteString(s.st.field("As-of", s.st.value.Render(dateTime(c.asOf))))
	b.WriteByte('\n')
	b.WriteString(s.st.field("Engine", s.st.value.Render(c.engine)) + "  " +
		s.st.field("Mode", s.st.value.Render(c.mode)) + "  " +
		s.st.field("Status", s.st.value.Render(c.status)))
	b.WriteString("\n\n")

	if !s.hasDetail {
		b.WriteString(s.st.muted.Render("loading decisions…"))
		return b.String()
	}
	if s.detail.err != nil {
		b.WriteString(s.st.bad.Render("error: " + truncate(s.detail.err.Error(), 50)))
		return b.String()
	}

	b.WriteString(s.st.label.Render("Decisions"))
	b.WriteByte('\n')
	if len(s.detail.decisions) == 0 {
		b.WriteString(s.st.muted.Render("(none)"))
	} else {
		for _, d := range s.detail.decisions {
			b.WriteString(s.decisionLine(d))
			b.WriteByte('\n')
		}
	}

	if len(s.detail.llmCalls) > 0 {
		b.WriteString("\n")
		b.WriteString(s.st.label.Render("Engine calls"))
		b.WriteByte('\n')
		for _, c := range s.detail.llmCalls {
			line := c.model + "  " + itoa(c.durationMS) + "ms"
			if c.errMsg != "" {
				line += "  " + s.st.bad.Render("err: "+truncate(c.errMsg, 40))
			}
			b.WriteString(s.st.value.Render(line))
			b.WriteByte('\n')
			if c.response != "" {
				b.WriteString(s.st.muted.Render("  " + truncate(oneLine(c.response), 70)))
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func (s *decisionsScreen) decisionLine(d decisionRow) string {
	name := displayName(d.ticker, d.uid)
	head := s.st.value.Render(padRight(truncate(name, 9), 10) +
		padRight(d.action.String(), 6) + padRight(itoa(d.qty)+"l", 6) +
		padRight(d.orderType.String(), 7))
	if d.limitPrice != nil {
		head += s.st.muted.Render("@" + formatDecimal(*d.limitPrice) + " ")
	}
	head += s.validation(d.validationStatus) +
		s.st.muted.Render("  conf "+strconv.FormatFloat(d.confidence, 'f', 2, 64))
	if d.rationale != "" {
		head += "\n  " + s.st.muted.Render(truncate(oneLine(d.rationale), 68))
	}
	return head
}

func (s *decisionsScreen) validation(status string) string {
	switch status {
	case "valid", "ok", "accepted":
		return s.st.good.Render(status)
	case "rejected", "invalid", "error":
		return s.st.bad.Render(status)
	case "":
		return s.st.muted.Render("—")
	default:
		return s.st.warn.Render(status)
	}
}

// oneLine collapses newlines/tabs to spaces so a multi-line blob renders on a
// single detail row.
func oneLine(s string) string {
	r := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
	return strings.TrimSpace(r.Replace(s))
}
