package tui

import (
	"strings"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// ordersScreen is screen 4 (DESIGN §9): the live (non-terminal) order intents
// with their state and age. 'c' requests a manual cancel through the
// consumer-owned CancelRequester, behind a small y/n confirm so a stray key
// press cannot pull an order.
type ordersScreen struct {
	rm        *readModel
	st        *styles
	clk       clock.Clock
	keys      keyMap
	canceller CancelRequester

	data     ordersData
	selected int
	loaded   bool

	confirming bool
	pending    string // client order id awaiting confirm
	note       string // transient result note
}

func newOrdersScreen(rm *readModel, st *styles, clk clock.Clock, keys keyMap, c CancelRequester) *ordersScreen {
	return &ordersScreen{rm: rm, st: st, clk: clk, keys: keys, canceller: c}
}

func (s *ordersScreen) Init() tea.Cmd    { return ordersCmd(s.rm) }
func (s *ordersScreen) refresh() tea.Cmd { return ordersCmd(s.rm) }
func (s *ordersScreen) Title() string    { return "Orders" }

func (s *ordersScreen) StatusHint() string {
	if s.confirming {
		return "cancel " + truncate(s.pending, 8) + "? y/n"
	}
	return s.note
}

func (s *ordersScreen) ScreenHelp() []key.Binding {
	return []key.Binding{s.keys.Up, s.keys.Down, s.keys.Cancel}
}

func (s *ordersScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case ordersMsg:
		s.data = m.data
		s.loaded = true
		if s.selected >= len(s.data.rows) {
			s.selected = clampMin(len(s.data.rows)-1, 0)
		}
		return s, nil
	case cancelResultMsg:
		if m.err != nil {
			s.note = "cancel failed: " + truncate(m.err.Error(), 40)
		} else {
			s.note = "cancel requested: " + truncate(m.clientOrderID, 12)
		}
		return s, s.refresh()
	case tea.KeyMsg:
		return s.handleKey(m)
	}
	return s, nil
}

func (s *ordersScreen) handleKey(m tea.KeyMsg) (screen, tea.Cmd) {
	if s.confirming {
		switch m.String() {
		case "y", "Y", "enter":
			id := s.pending
			s.confirming = false
			s.pending = ""
			return s, cancelCmd(s.canceller, id)
		default: // n, esc, anything else aborts
			s.confirming = false
			s.pending = ""
			s.note = "cancel aborted"
		}
		return s, nil
	}
	switch {
	case key.Matches(m, s.keys.Down):
		s.move(1)
	case key.Matches(m, s.keys.Up):
		s.move(-1)
	case key.Matches(m, s.keys.Cancel):
		if id := s.selectedID(); id != "" {
			s.confirming = true
			s.pending = id
			s.note = ""
		}
	}
	return s, nil
}

func (s *ordersScreen) move(delta int) {
	if len(s.data.rows) == 0 {
		return
	}
	s.selected += delta
	if s.selected < 0 {
		s.selected = 0
	}
	if s.selected >= len(s.data.rows) {
		s.selected = len(s.data.rows) - 1
	}
}

func (s *ordersScreen) selectedID() string {
	if s.selected < 0 || s.selected >= len(s.data.rows) {
		return ""
	}
	return s.data.rows[s.selected].clientOrderID
}

func (s *ordersScreen) View(width, height int) string {
	return s.st.twoPane("Open orders", s.listBody(), "Order", s.detailBody(), width, height, true)
}

func (s *ordersScreen) listBody() string {
	if !s.loaded {
		return s.st.muted.Render("loading…")
	}
	if s.data.err != nil {
		return s.st.bad.Render("error: " + truncate(s.data.err.Error(), 40))
	}
	now := s.clk.Now()
	rows := make([]string, len(s.data.rows))
	for i, r := range s.data.rows {
		rows[i] = padRight(truncate(displayName(r.ticker, r.uid), 8), 9) +
			padRight(r.side.String(), 5) +
			padRight(itoa(r.qty)+"l", 5) +
			padRight(r.state.String(), 10) +
			ago(now, r.createdAt)
	}
	return s.st.renderRows(rows, s.selected, minInnerWidth, 100)
}

func (s *ordersScreen) detailBody() string {
	if !s.loaded || len(s.data.rows) == 0 {
		return s.st.muted.Render("no open orders")
	}
	r := s.data.rows[s.selected]
	now := s.clk.Now()
	var b strings.Builder
	b.WriteString(s.st.heading.Render(displayName(r.ticker, r.uid)))
	b.WriteByte('\n')
	b.WriteString(s.st.field("Client ID", s.st.muted.Render(r.clientOrderID)))
	b.WriteByte('\n')
	b.WriteString(s.st.field("Side", s.st.value.Render(r.side.String())) + "  " +
		s.st.field("Qty", s.st.value.Render(itoa(r.qty)+" lots")))
	b.WriteByte('\n')
	orderType := r.orderType.String()
	if r.limitPrice != nil {
		orderType += " @ " + formatDecimal(*r.limitPrice)
	}
	b.WriteString(s.st.field("Type", s.st.value.Render(orderType)))
	b.WriteByte('\n')
	b.WriteString(s.st.field("State", s.st.value.Render(r.state.String())))
	b.WriteByte('\n')
	b.WriteString(s.st.field("Age", s.st.value.Render(ago(now, r.createdAt))))
	b.WriteString("\n\n")
	if s.confirming && s.pending == r.clientOrderID {
		b.WriteString(s.st.bad.Render("Cancel this order? press y to confirm, n to abort"))
	} else {
		b.WriteString(s.st.muted.Render("press c to cancel this order"))
	}
	return b.String()
}
