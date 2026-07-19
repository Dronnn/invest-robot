package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap is the full set of TUI keybindings. Global keys are handled by the
// root model; screen-scoped keys (cancel, filter) are handled by the owning
// screen and shown in help only when that screen is active.
type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Tab    key.Binding
	Prev   key.Binding
	Nums   key.Binding
	Pause  key.Binding
	Kill   key.Binding
	Help   key.Binding
	Quit   key.Binding
	Cancel key.Binding
	Filter key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next screen")),
		Prev:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev screen")),
		Nums:   key.NewBinding(key.WithKeys("1", "2", "3", "4", "5"), key.WithHelp("1-5", "jump to screen")),
		Pause:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause/resume")),
		Kill:   key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "kill switch")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Cancel: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "cancel order")),
		Filter: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter level")),
	}
}

// ShortHelp implements help.KeyMap — the single-line hint set.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.Up, k.Down, k.Pause, k.Kill, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap — the expanded overlay, grouped by column.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Tab, k.Prev, k.Nums},
		{k.Up, k.Down},
		{k.Pause, k.Kill},
		{k.Cancel, k.Filter},
		{k.Help, k.Quit},
	}
}
