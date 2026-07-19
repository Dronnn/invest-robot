// Package tui is the robot's Bubble Tea terminal UI: a lazygit-style dashboard
// over the running trading engine's state. It is a read-only client of app
// state (DESIGN §3) — it never owns the decision loop and the engine runs
// identically headless.
//
// Composition follows the standard Bubble Tea pattern: a rootModel owns the tab
// bar, status bar (with the PAPER/BACKTEST/REAL mode badge, DESIGN §10), global
// keybindings, the help overlay and the kill-switch confirm modal, and forwards
// everything else to one of five screen models — Dashboard, Positions,
// Decisions (the cycle replay), Orders and Log (DESIGN §9).
//
// All data is read and shaped by the package-internal readModel, always inside
// a tea.Cmd (off the UI goroutine) with a context-bounded query, and delivered
// back as messages. The UI loop never blocks on DB/IO. Data refreshes on a
// periodic tea.Tick (default 2s, configurable via Deps).
//
// Engine control (pause/resume/kill) and manual order cancellation go through
// the consumer-owned EngineController and CancelRequester interfaces; the real
// cycle/execution packages implement them later. StubController and
// StubCancelRequester ship for tests and dev wiring.
package tui
