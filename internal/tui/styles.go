package tui

import (
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/charmbracelet/lipgloss"
)

// styles holds every lipgloss style the TUI renders with, built once per App.
// Colours are AdaptiveColor pairs (light-terminal / dark-terminal) and plain
// ANSI where possible, so the theme degrades gracefully on both backgrounds and
// on colour-poor terminals (lipgloss down-samples truecolor to the detected
// profile automatically).
type styles struct {
	// Chrome.
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style
	tabBar      lipgloss.Style
	statusBar   lipgloss.Style
	statusKey   lipgloss.Style // key hints in the status bar

	// Panes.
	paneActive   lipgloss.Style // bordered box, focused
	paneInactive lipgloss.Style // bordered box, unfocused
	paneTitle    lipgloss.Style

	// List rows.
	rowSelected lipgloss.Style
	rowNormal   lipgloss.Style

	// Text emphasis.
	label   lipgloss.Style // dim field labels
	value   lipgloss.Style // normal values
	heading lipgloss.Style // section headings
	muted   lipgloss.Style
	good    lipgloss.Style // positive / healthy (green)
	bad     lipgloss.Style // negative / error (red)
	warn    lipgloss.Style // caution (amber)

	// Mode badges.
	badgePaper    lipgloss.Style
	badgeBacktest lipgloss.Style
	badgeReal     lipgloss.Style

	// Overlay (help, confirm modal).
	overlay      lipgloss.Style
	overlayTitle lipgloss.Style
	confirmInput lipgloss.Style
}

var (
	colGood    = lipgloss.AdaptiveColor{Light: "22", Dark: "42"}   // green
	colBad     = lipgloss.AdaptiveColor{Light: "124", Dark: "203"} // red
	colWarn    = lipgloss.AdaptiveColor{Light: "130", Dark: "214"} // amber
	colMuted   = lipgloss.AdaptiveColor{Light: "244", Dark: "245"}
	colAccent  = lipgloss.AdaptiveColor{Light: "25", Dark: "39"} // blue
	colBorder  = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
	colBorderF = lipgloss.AdaptiveColor{Light: "25", Dark: "39"} // focused border
	colSelBg   = lipgloss.AdaptiveColor{Light: "254", Dark: "236"}
	colSelFg   = lipgloss.AdaptiveColor{Light: "232", Dark: "252"}
	colHeading = lipgloss.AdaptiveColor{Light: "25", Dark: "111"}
)

// newStyles builds the theme.
func newStyles() *styles {
	s := &styles{}

	s.tabInactive = lipgloss.NewStyle().Padding(0, 2).Foreground(colMuted)
	s.tabActive = lipgloss.NewStyle().Padding(0, 2).Bold(true).
		Foreground(colSelFg).Background(colAccent)
	s.tabBar = lipgloss.NewStyle()

	s.statusBar = lipgloss.NewStyle().Foreground(colMuted)
	s.statusKey = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	s.paneActive = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(colBorderF)
	s.paneInactive = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(colBorder)
	s.paneTitle = lipgloss.NewStyle().Bold(true).Foreground(colHeading)

	s.rowSelected = lipgloss.NewStyle().Bold(true).
		Foreground(colSelFg).Background(colSelBg)
	s.rowNormal = lipgloss.NewStyle()

	s.label = lipgloss.NewStyle().Foreground(colMuted)
	s.value = lipgloss.NewStyle()
	s.heading = lipgloss.NewStyle().Bold(true).Foreground(colHeading)
	s.muted = lipgloss.NewStyle().Foreground(colMuted)
	s.good = lipgloss.NewStyle().Foreground(colGood)
	s.bad = lipgloss.NewStyle().Foreground(colBad)
	s.warn = lipgloss.NewStyle().Foreground(colWarn)

	s.badgePaper = lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(colSelFg).Background(colAccent)
	s.badgeBacktest = lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(lipgloss.Color("232")).Background(colWarn)
	// REAL is loud: bold white on bright red, per DESIGN §10.
	s.badgeReal = lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(lipgloss.Color("231")).Background(lipgloss.Color("196"))

	s.overlay = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(colBorderF).Padding(1, 2)
	s.overlayTitle = lipgloss.NewStyle().Bold(true).Foreground(colHeading)
	s.confirmInput = lipgloss.NewStyle().Bold(true).Foreground(colBad)

	return s
}

// badge renders the mode badge for the status bar.
func (s *styles) badge(mode string) string {
	switch normalizeMode(mode) {
	case ModeReal:
		return s.badgeReal.Render(" " + ModeReal + " ")
	case ModeBacktest:
		return s.badgeBacktest.Render(ModeBacktest)
	case ModePaper:
		return s.badgePaper.Render(ModePaper)
	default:
		return s.badgeBacktest.Render(normalizeMode(mode))
	}
}

// signStyle picks the colour for a signed value: green positive, red negative,
// muted zero.
func (s *styles) signStyle(d model.Decimal) lipgloss.Style {
	switch d.Sign() {
	case 1:
		return s.good
	case -1:
		return s.bad
	default:
		return s.muted
	}
}

// engineStateLabel renders an engine state with a state-appropriate colour.
func (s *styles) engineStateLabel(state string) string {
	switch state {
	case EngineRunning:
		return s.good.Render("running")
	case EnginePaused:
		return s.warn.Render("paused")
	case EngineHalted:
		return s.bad.Render("HALTED")
	default:
		return s.muted.Render(state)
	}
}

// levelStyle colours a log level.
func (s *styles) levelStyle(level string) lipgloss.Style {
	switch level {
	case "error", "fatal", "crit":
		return s.bad
	case "warn", "warning":
		return s.warn
	case "info":
		return s.value
	default:
		return s.muted
	}
}
