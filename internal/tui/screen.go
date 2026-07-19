package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// screen is one tab's model. Screens are composed under rootModel (standard
// Bubble Tea composition): the root owns chrome, global keys and overlays and
// forwards everything else here. A screen never blocks — all data comes back
// asynchronously via the data messages its Init/refresh commands produce.
type screen interface {
	// Init returns the command that loads the screen's data for the first time.
	Init() tea.Cmd
	// refresh returns the command that reloads the screen's data (called on the
	// periodic tick and when the screen becomes active).
	refresh() tea.Cmd
	// Update handles a message (a key already filtered of global bindings, a
	// data message, or a tick) and returns the updated screen plus any command.
	Update(msg tea.Msg) (screen, tea.Cmd)
	// View renders the screen body into a content area of the given size.
	View(width, height int) string
	// Title is the tab label.
	Title() string
	// ScreenHelp returns screen-scoped keybindings for the help overlay.
	ScreenHelp() []key.Binding
	// StatusHint returns a short right-aligned status note (or "").
	StatusHint() string
}

// minInnerWidth / minInnerHeight guard the pane math against degenerate
// terminal sizes so View never produces negative dimensions or panics.
const (
	minInnerWidth  = 8
	minInnerHeight = 2
)

// clampMin returns v, or lo if v < lo.
func clampMin(v, lo int) int {
	if v < lo {
		return lo
	}
	return v
}

// pane draws a bordered box of the given OUTER width/height (border included)
// with a title line, focused or not. body is expected to already fit; excess
// lines/columns are clipped by the enclosing style.
func (s *styles) pane(title, body string, width, height int, focused bool) string {
	innerW := clampMin(width-2, minInnerWidth)
	innerH := clampMin(height-2, minInnerHeight)

	st := s.paneInactive
	if focused {
		st = s.paneActive
	}
	var content strings.Builder
	if title != "" {
		content.WriteString(s.paneTitle.Render(truncate(title, innerW)))
		content.WriteByte('\n')
	}
	content.WriteString(body)
	return st.Width(innerW).Height(innerH).MaxHeight(height).Render(content.String())
}

// twoPane composes a left list pane and a right detail pane side by side,
// filling width×height. The left pane takes ~40% (clamped), the right the rest.
func (s *styles) twoPane(leftTitle, left, rightTitle, right string, width, height int, leftFocused bool) string {
	leftW := width * 2 / 5
	leftW = clampMin(leftW, minInnerWidth+2)
	if leftW > width-(minInnerWidth+2) {
		leftW = width - (minInnerWidth + 2)
	}
	rightW := width - leftW
	l := s.pane(leftTitle, left, leftW, height, leftFocused)
	r := s.pane(rightTitle, right, rightW, height, !leftFocused)
	return lipgloss.JoinHorizontal(lipgloss.Top, l, r)
}

// listWindow returns the [start,end) slice of a list of total rows that should
// be visible given a viewport of height rows and the selected index, keeping the
// selection in view.
func listWindow(total, height, selected int) (int, int) {
	if height <= 0 || total <= 0 {
		return 0, 0
	}
	if total <= height {
		return 0, total
	}
	start := selected - height/2
	if start < 0 {
		start = 0
	}
	if start+height > total {
		start = total - height
	}
	return start, start + height
}

// renderRows renders plain-text rows into a viewport of innerW×innerH, drawing
// the selected row with the selection style and windowing around it. Rows are
// truncated to innerW BEFORE styling so the colour codes never break the width
// math. selected < 0 disables the highlight (unfocused/no-selection lists).
func (s *styles) renderRows(rows []string, selected, innerW, innerH int) string {
	if len(rows) == 0 {
		return s.muted.Render("(none)")
	}
	start, end := listWindow(len(rows), innerH, selected)
	var b strings.Builder
	for i := start; i < end; i++ {
		text := truncate(rows[i], innerW)
		if i == selected {
			b.WriteString(s.rowSelected.Width(innerW).Render(text))
		} else {
			b.WriteString(s.rowNormal.Render(text))
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// field renders a "label value" pair for the dashboard-style key/value lists.
func (s *styles) field(label, value string) string {
	return s.label.Render(label+": ") + value
}
