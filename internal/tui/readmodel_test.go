package tui

import (
	"context"
	"testing"
)

func TestReadModelDashboard(t *testing.T) {
	db := openTestDB(t)
	seedFullScenario(t, db)
	rm := newTestReadModel(db)

	d := rm.dashboard(context.Background())
	if d.err != nil {
		t.Fatalf("dashboard err: %v", d.err)
	}
	if !d.equityKnown {
		t.Fatalf("equity should be known, warn=%q", d.warn)
	}
	// cash 100000 + 2 lots * 10 * 150 = 103000.
	if got := formatDecimal(d.equity); got != "103,000" {
		t.Errorf("equity = %q, want 103,000", got)
	}
	if got := formatDecimal(d.cash); got != "100,000" {
		t.Errorf("cash = %q, want 100,000", got)
	}
	if d.positionCount != 1 {
		t.Errorf("positionCount = %d, want 1", d.positionCount)
	}
	if !d.dayPnLKnown {
		t.Fatalf("day pnl should be known")
	}
	if got := formatDecimal(d.dayTotal); got != "5,000" {
		t.Errorf("day total = %q, want 5,000", got)
	}
}

func TestReadModelDashboardEmptyDB(t *testing.T) {
	db := openTestDB(t)
	rm := newTestReadModel(db)

	d := rm.dashboard(context.Background())
	if d.err != nil {
		t.Fatalf("empty dashboard err: %v", d.err)
	}
	if !d.equityKnown || !d.cash.IsZero() || !d.equity.IsZero() {
		t.Errorf("empty: equityKnown=%v cash=%s equity=%s", d.equityKnown, d.cash, d.equity)
	}
	if d.positionCount != 0 {
		t.Errorf("empty positionCount = %d", d.positionCount)
	}
	if d.dayPnLKnown {
		t.Errorf("empty day pnl should be unknown (no snapshot)")
	}
}

func TestReadModelPositions(t *testing.T) {
	db := openTestDB(t)
	ids := seedFullScenario(t, db)
	rm := newTestReadModel(db)

	d := rm.positions(context.Background())
	if d.err != nil {
		t.Fatalf("positions err: %v", d.err)
	}
	if len(d.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(d.rows))
	}
	r := d.rows[0]
	if r.uid != ids.uid || r.ticker != "SBER" || r.qty != 2 || !r.priced {
		t.Errorf("row = %+v", r)
	}
	if got := formatDecimal(r.lastPrice); got != "150" {
		t.Errorf("last = %q, want 150", got)
	}
	// pnl = (150-100)*2*10 = 1000.
	if got := formatDecimal(r.pnl); got != "1,000" {
		t.Errorf("pnl = %q, want 1,000", got)
	}
}

func TestReadModelPositionsEmpty(t *testing.T) {
	db := openTestDB(t)
	rm := newTestReadModel(db)
	d := rm.positions(context.Background())
	if d.err != nil || len(d.rows) != 0 {
		t.Fatalf("empty positions: err=%v rows=%d", d.err, len(d.rows))
	}
}

func TestReadModelFills(t *testing.T) {
	db := openTestDB(t)
	ids := seedFullScenario(t, db)
	rm := newTestReadModel(db)

	fills, err := rm.fills(context.Background(), ids.uid)
	if err != nil {
		t.Fatalf("fills err: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("fills = %d, want 1", len(fills))
	}
	if fills[0].intentID != ids.clientOrderID || fills[0].qty != 1 {
		t.Errorf("fill = %+v", fills[0])
	}
	if got := formatDecimal(fills[0].price); got != "150" {
		t.Errorf("fill price = %q", got)
	}
}

func TestReadModelCyclesAndDetail(t *testing.T) {
	db := openTestDB(t)
	ids := seedFullScenario(t, db)
	rm := newTestReadModel(db)

	cs := rm.cycles(context.Background())
	if cs.err != nil || len(cs.rows) != 1 {
		t.Fatalf("cycles: err=%v rows=%d", cs.err, len(cs.rows))
	}
	if cs.rows[0].id != ids.cycleID || cs.rows[0].engine != "rules" {
		t.Errorf("cycle row = %+v", cs.rows[0])
	}

	detail := rm.cycleDetail(context.Background(), ids.cycleID)
	if detail.err != nil {
		t.Fatalf("detail err: %v", detail.err)
	}
	if len(detail.decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(detail.decisions))
	}
	d := detail.decisions[0]
	if d.ticker != "SBER" || d.action.String() != "buy" || d.validationStatus != "valid" {
		t.Errorf("decision = %+v", d)
	}
	if len(detail.llmCalls) != 1 || detail.llmCalls[0].model != "rules" {
		t.Errorf("llm calls = %+v", detail.llmCalls)
	}
}

func TestReadModelOrders(t *testing.T) {
	db := openTestDB(t)
	ids := seedFullScenario(t, db)
	rm := newTestReadModel(db)

	d := rm.orders(context.Background())
	if d.err != nil || len(d.rows) != 1 {
		t.Fatalf("orders: err=%v rows=%d", d.err, len(d.rows))
	}
	r := d.rows[0]
	if r.clientOrderID != ids.clientOrderID || r.ticker != "SBER" || r.state.String() != "new" {
		t.Errorf("order row = %+v", r)
	}
}

func TestReadModelEvents(t *testing.T) {
	db := openTestDB(t)
	seedFullScenario(t, db)
	rm := newTestReadModel(db)

	d := rm.events(context.Background())
	if d.err != nil {
		t.Fatalf("events err: %v", d.err)
	}
	if len(d.rows) != 3 {
		t.Fatalf("events = %d, want 3", len(d.rows))
	}
	// Most recent first: error, warn, info.
	if d.rows[0].level != "error" || d.rows[0].code != "order_rejected" {
		t.Errorf("newest event = %+v", d.rows[0])
	}
}

func TestReadModelEmptyEventsNoPanic(t *testing.T) {
	db := openTestDB(t)
	rm := newTestReadModel(db)
	if d := rm.events(context.Background()); d.err != nil || len(d.rows) != 0 {
		t.Fatalf("empty events: err=%v rows=%d", d.err, len(d.rows))
	}
	if cs := rm.cycles(context.Background()); cs.err != nil || len(cs.rows) != 0 {
		t.Fatalf("empty cycles: err=%v rows=%d", cs.err, len(cs.rows))
	}
	if o := rm.orders(context.Background()); o.err != nil || len(o.rows) != 0 {
		t.Fatalf("empty orders: err=%v rows=%d", o.err, len(o.rows))
	}
}
