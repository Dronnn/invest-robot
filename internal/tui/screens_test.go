package tui

import (
	"strings"
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
	tea "github.com/charmbracelet/bubbletea"
)

func sizedApp(t *testing.T, w, h int) (*rootModel, *StubController, *StubCancelRequester) {
	t.Helper()
	db := openTestDB(t)
	app, ctrl, canceller := newTestApp(t, db)
	app.root.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return app.root, ctrl, canceller
}

func TestViewBeforeSize(t *testing.T) {
	db := openTestDB(t)
	app, _, _ := newTestApp(t, db)
	if got := app.root.View(); got != "initializing…" {
		t.Fatalf("pre-size view = %q", got)
	}
}

func TestViewNoPanicTinySize(t *testing.T) {
	m, _, _ := sizedApp(t, 2, 2)
	_ = m.View() // must not panic
	m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	_ = m.View()
}

func TestDashboardViewSubstrings(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(dashboardMsg{data: dashboardData{
		cash: model.MustDecimal("1000"), equity: model.MustDecimal("103000"), equityKnown: true,
		dayTotal: model.MustDecimal("5000"), dayPnLKnown: true, positionCount: 2, healthKnown: false,
	}})
	view := m.View()
	for _, want := range []string{"Dashboard", "Account", "Equity", "103,000", "Cash", "PAPER"} {
		if !strings.Contains(view, want) {
			t.Errorf("dashboard view missing %q\n%s", want, view)
		}
	}
}

func TestModeBadgeReal(t *testing.T) {
	m, ctrl, _ := sizedApp(t, 100, 30)
	ctrl.SetStatus(EngineStatus{Mode: "REAL"})
	m.Update(dashboardMsg{data: dashboardData{equityKnown: true}})
	if view := m.View(); !strings.Contains(view, "REAL") {
		t.Errorf("REAL badge not shown in status bar\n%s", view)
	}
}

func TestPositionsViewSubstrings(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(keyRunes('2')) // activate Positions
	m.Update(positionsMsg{data: positionsData{rows: []positionRow{
		{uid: "uid-sber", ticker: "SBER", qty: 2, avgPrice: model.MustDecimal("100"),
			lastPrice: model.MustDecimal("150"), priced: true, pnl: model.MustDecimal("1000")},
	}}})
	view := m.View()
	for _, want := range []string{"Positions", "SBER", "Fills"} {
		if !strings.Contains(view, want) {
			t.Errorf("positions view missing %q\n%s", want, view)
		}
	}
}

func TestPositionsNavigationMovesSelection(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(keyRunes('2'))
	m.Update(positionsMsg{data: positionsData{rows: []positionRow{
		{uid: "a", ticker: "AAA", qty: 1}, {uid: "b", ticker: "BBB", qty: 1},
	}}})
	ps := m.screens[screenPositions].(*positionsScreen)
	if ps.selected != 0 {
		t.Fatalf("initial selected = %d", ps.selected)
	}
	m.Update(keyType(tea.KeyDown))
	if ps.selected != 1 {
		t.Fatalf("after down selected = %d, want 1", ps.selected)
	}
	m.Update(keyType(tea.KeyUp))
	if ps.selected != 0 {
		t.Fatalf("after up selected = %d, want 0", ps.selected)
	}
}

func TestDecisionsNavigationLoadsDetail(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(keyRunes('3'))
	_, cmd := m.Update(cyclesMsg{data: decisionsData{rows: []cycleRow{{id: 1, engine: "rules"}, {id: 2, engine: "rules"}}}})
	if cmd == nil {
		t.Fatalf("cycles load should request detail for the selected cycle")
	}
	ds := m.screens[screenDecisions].(*decisionsScreen)
	m.Update(keyType(tea.KeyDown))
	if ds.selected != 1 {
		t.Fatalf("after down selected = %d, want 1", ds.selected)
	}
}

func TestOrdersCancelFlow(t *testing.T) {
	m, _, canceller := sizedApp(t, 100, 30)
	m.Update(keyRunes('4')) // activate Orders
	m.Update(ordersMsg{data: ordersData{rows: []orderRow{
		{clientOrderID: "abc", uid: "u", ticker: "SBER", side: model.SideBuy, qty: 1, state: model.IntentNew},
	}}})
	os := m.screens[screenOrders].(*ordersScreen)

	m.Update(keyRunes('c'))
	if !os.confirming || os.pending != "abc" {
		t.Fatalf("c should arm confirm: confirming=%v pending=%q", os.confirming, os.pending)
	}
	_, cmd := m.Update(keyRunes('y'))
	if cmd == nil {
		t.Fatalf("y should emit cancel command")
	}
	msg := cmd()
	if cr, ok := msg.(cancelResultMsg); !ok || cr.clientOrderID != "abc" {
		t.Fatalf("cancel command produced %#v", msg)
	}
	if got := canceller.Requests(); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("canceller requests = %v", got)
	}
}

func TestOrdersCancelAbort(t *testing.T) {
	m, _, canceller := sizedApp(t, 100, 30)
	m.Update(keyRunes('4'))
	m.Update(ordersMsg{data: ordersData{rows: []orderRow{{clientOrderID: "abc", state: model.IntentNew}}}})
	os := m.screens[screenOrders].(*ordersScreen)
	m.Update(keyRunes('c'))
	m.Update(keyRunes('n')) // abort
	if os.confirming {
		t.Fatalf("n should abort confirm")
	}
	if len(canceller.Requests()) != 0 {
		t.Fatalf("abort must not request cancel")
	}
}

func TestLogFilterCycle(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(keyRunes('5')) // activate Log
	m.Update(logMsg{data: logData{rows: []eventRow{
		{level: "error", code: "a"}, {level: "warn", code: "b"}, {level: "info", code: "c"},
	}}})
	ls := m.screens[screenLog].(*logScreen)
	if got := len(ls.filtered()); got != 3 {
		t.Fatalf("all filter = %d rows, want 3", got)
	}
	m.Update(keyRunes('f')) // -> error only
	if got := len(ls.filtered()); got != 1 {
		t.Fatalf("error filter = %d rows, want 1", got)
	}
	m.Update(keyRunes('f')) // -> warn+
	if got := len(ls.filtered()); got != 2 {
		t.Fatalf("warn filter = %d rows, want 2", got)
	}
}

func TestLogViewSubstrings(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(keyRunes('5'))
	m.Update(logMsg{data: logData{rows: []eventRow{{level: "error", code: "order_rejected"}}}})
	view := m.View()
	for _, want := range []string{"Log", "ERROR", "order_rejected", "filter"} {
		if !strings.Contains(view, want) {
			t.Errorf("log view missing %q\n%s", want, view)
		}
	}
}

func TestHelpOverlayView(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(keyRunes('?'))
	view := m.View()
	for _, want := range []string{"Help", "quit", "kill switch"} {
		if !strings.Contains(view, want) {
			t.Errorf("help view missing %q\n%s", want, view)
		}
	}
}

func TestKillModalView(t *testing.T) {
	m, _, _ := sizedApp(t, 100, 30)
	m.Update(keyRunes('K'))
	view := m.View()
	for _, want := range []string{"KILL SWITCH", "flatten-only", "Esc"} {
		if !strings.Contains(view, want) {
			t.Errorf("kill modal missing %q\n%s", want, view)
		}
	}
}
