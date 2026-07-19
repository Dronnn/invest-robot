package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(r ...rune) tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: r} }
func keyType(k tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: k} }

func TestRootTabSwitching(t *testing.T) {
	db := openTestDB(t)
	app, _, _ := newTestApp(t, db)
	m := app.root

	if m.active != screenDashboard {
		t.Fatalf("initial active = %d", m.active)
	}
	m.Update(keyType(tea.KeyTab))
	if m.active != screenPositions {
		t.Fatalf("after tab active = %d, want %d", m.active, screenPositions)
	}
	// Jump by digit.
	m.Update(keyRunes('3'))
	if m.active != screenDecisions {
		t.Fatalf("after '3' active = %d, want %d", m.active, screenDecisions)
	}
	m.Update(keyType(tea.KeyShiftTab))
	if m.active != screenPositions {
		t.Fatalf("after shift+tab active = %d, want %d", m.active, screenPositions)
	}
	// Wrap: from Positions go back one to Dashboard, then shift+tab wraps to Log.
	m.Update(keyType(tea.KeyShiftTab))
	m.Update(keyType(tea.KeyShiftTab))
	if m.active != screenLog {
		t.Fatalf("wrap active = %d, want %d", m.active, screenLog)
	}
	m.Update(keyType(tea.KeyTab))
	if m.active != screenDashboard {
		t.Fatalf("wrap forward active = %d, want %d", m.active, screenDashboard)
	}
}

func TestRootPauseToggle(t *testing.T) {
	db := openTestDB(t)
	app, ctrl, _ := newTestApp(t, db)
	m := app.root

	m.Update(keyRunes('p'))
	if ctrl.Pauses != 1 || ctrl.Status().State != EnginePaused {
		t.Fatalf("after p: pauses=%d state=%q", ctrl.Pauses, ctrl.Status().State)
	}
	m.Update(keyRunes('p'))
	if ctrl.Resumes != 1 || ctrl.Status().State != EngineRunning {
		t.Fatalf("after p again: resumes=%d state=%q", ctrl.Resumes, ctrl.Status().State)
	}
}

func TestRootKillSwitchRejectsWrongPhrase(t *testing.T) {
	db := openTestDB(t)
	app, ctrl, _ := newTestApp(t, db)
	m := app.root

	m.Update(keyRunes('K'))
	if !m.killConfirming {
		t.Fatalf("K should enter confirm mode")
	}
	// A wrong phrase must NOT trigger the kill switch.
	m.Update(keyRunes('N'))
	m.Update(keyRunes('O'))
	m.Update(keyType(tea.KeyEnter))
	if ctrl.Kills != 0 {
		t.Fatalf("wrong phrase engaged kill switch: kills=%d", ctrl.Kills)
	}
	if m.killConfirming {
		t.Fatalf("confirm mode should have exited after Enter")
	}
}

func TestRootKillSwitchAcceptsPhrase(t *testing.T) {
	db := openTestDB(t)
	app, ctrl, _ := newTestApp(t, db)
	m := app.root

	m.Update(keyRunes('K'))
	for _, r := range "KILL" {
		m.Update(keyRunes(r))
	}
	m.Update(keyType(tea.KeyEnter))
	if ctrl.Kills != 1 || ctrl.Status().State != EngineHalted {
		t.Fatalf("correct phrase: kills=%d state=%q", ctrl.Kills, ctrl.Status().State)
	}
	if m.killConfirming {
		t.Fatalf("confirm mode should have exited")
	}
}

func TestRootKillSwitchEscAborts(t *testing.T) {
	db := openTestDB(t)
	app, ctrl, _ := newTestApp(t, db)
	m := app.root

	m.Update(keyRunes('K'))
	m.Update(keyRunes('K'))
	m.Update(keyType(tea.KeyEsc))
	if m.killConfirming || ctrl.Kills != 0 {
		t.Fatalf("esc should abort: confirming=%v kills=%d", m.killConfirming, ctrl.Kills)
	}
}

func TestRootKillConfirmSuppressesQuit(t *testing.T) {
	db := openTestDB(t)
	app, _, _ := newTestApp(t, db)
	m := app.root

	m.Update(keyRunes('K'))
	// 'q' while confirming must be captured as buffer input, not a quit.
	_, cmd := m.Update(keyRunes('q'))
	if cmd != nil {
		t.Fatalf("q during kill-confirm should not emit a command (quit)")
	}
	if !m.killConfirming || m.killBuffer != "q" {
		t.Fatalf("q should append to buffer: confirming=%v buffer=%q", m.killConfirming, m.killBuffer)
	}
}

func TestRootHelpToggle(t *testing.T) {
	db := openTestDB(t)
	app, _, _ := newTestApp(t, db)
	m := app.root

	m.Update(keyRunes('?'))
	if !m.showHelp {
		t.Fatalf("? should open help")
	}
	m.Update(keyType(tea.KeyEnter))
	if m.showHelp {
		t.Fatalf("any key should close help")
	}
}

func TestRootQuit(t *testing.T) {
	db := openTestDB(t)
	app, _, _ := newTestApp(t, db)
	m := app.root

	for _, k := range []tea.KeyMsg{keyRunes('q'), keyType(tea.KeyCtrlC)} {
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatalf("%v should emit quit command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%v command did not produce QuitMsg", k)
		}
	}
}

func TestRootRoutesDataToOwningScreen(t *testing.T) {
	db := openTestDB(t)
	app, _, _ := newTestApp(t, db)
	m := app.root

	// A logMsg must reach the log screen even when it is not active.
	m.Update(logMsg{data: logData{rows: []eventRow{{level: "info", code: "x"}}}})
	lg := m.screens[screenLog].(*logScreen)
	if !lg.loaded || len(lg.data.rows) != 1 {
		t.Fatalf("logMsg not routed: loaded=%v rows=%d", lg.loaded, len(lg.data.rows))
	}
}
