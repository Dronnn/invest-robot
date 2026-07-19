package rules

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/decision"
	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/model"
)

var update = flag.Bool("update", false, "update golden test files")

const (
	goldenRequestPath  = "testdata/golden_request.json"
	goldenResponsePath = "testdata/golden_response.json"
)

// goldenRequest builds a fixed, realistic multi-instrument Request covering
// an entry, an exit, a plain hold, and a stale-data skip in one cycle. It is
// the source of truth for testdata/golden_request.json (regenerate with
// -update); both files are committed so the fixture and the expected output
// are both reviewable in a diff.
func goldenRequest() decision.Request {
	asOf := time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC)

	return decision.Request{
		AsOf: asOf,
		Mode: "paper",
		Cycle: decision.CycleMeta{
			ID:        42,
			StartedAt: asOf,
			Interval:  "5m",
		},
		Portfolio: decision.Portfolio{
			Cash:   model.MustDecimal("500000"),
			Equity: model.MustDecimal("1000000"),
			Positions: []decision.PositionView{
				{
					UID:           "GAZP-UID",
					Ticker:        "GAZP",
					Qty:           20,
					AvgPrice:      model.MustDecimal("150"),
					LastPrice:     model.MustDecimal("140"),
					UnrealizedPnL: model.MustDecimal("-200"),
				},
			},
		},
		Instruments: []decision.InstrumentContext{
			{ // entry: bullish EMA cross, RSI confirming
				UID: "SBER-UID", FIGI: "BBG000ABC1", Ticker: "SBER", ClassCode: "TQBR",
				Lot: 10, MinPriceIncrement: model.MustDecimal("0.01"),
				Quote: decision.QuoteView{
					Bid: model.MustDecimal("249.90"), Ask: model.MustDecimal("250.10"), Last: model.MustDecimal("250"), TS: asOf,
				},
				Features: features.Snapshot{
					UID: "SBER-UID", Interval: model.Interval5m, AsOf: asOf,
					LastClose: model.MustDecimal("250"), Volume: 125000,
					SMA: 248, EMAFast: 251, EMASlow: 245, RSI: 60, ATR: 2.5,
					EMATrend: features.EMABullish, RSIZone: features.RSINeutral,
					Params: features.DefaultParams(),
				},
				DataFreshness: 30 * time.Second,
			},
			{ // exit: existing position, bearish EMA cross
				UID: "GAZP-UID", FIGI: "BBG000ABC2", Ticker: "GAZP", ClassCode: "TQBR",
				Lot: 10, MinPriceIncrement: model.MustDecimal("0.01"),
				Quote: decision.QuoteView{
					Bid: model.MustDecimal("139.90"), Ask: model.MustDecimal("140.10"), Last: model.MustDecimal("140"), TS: asOf,
				},
				Features: features.Snapshot{
					UID: "GAZP-UID", Interval: model.Interval5m, AsOf: asOf,
					LastClose: model.MustDecimal("140"), Volume: 98000,
					SMA: 143, EMAFast: 138, EMASlow: 142, RSI: 45, ATR: 3.1,
					EMATrend: features.EMABearish, RSIZone: features.RSINeutral,
					Params: features.DefaultParams(),
				},
				DataFreshness: 45 * time.Second,
			},
			{ // hold: no position, no entry signal (EMA bearish)
				UID: "LKOH-UID", FIGI: "BBG000ABC3", Ticker: "LKOH", ClassCode: "TQBR",
				Lot: 1, MinPriceIncrement: model.MustDecimal("0.5"),
				Quote: decision.QuoteView{
					Bid: model.MustDecimal("5999"), Ask: model.MustDecimal("6001"), Last: model.MustDecimal("6000"), TS: asOf,
				},
				Features: features.Snapshot{
					UID: "LKOH-UID", Interval: model.Interval5m, AsOf: asOf,
					LastClose: model.MustDecimal("6000"), Volume: 5000,
					SMA: 6050, EMAFast: 5990, EMASlow: 6040, RSI: 38, ATR: 55,
					EMATrend: features.EMABearish, RSIZone: features.RSINeutral,
					Params: features.DefaultParams(),
				},
				DataFreshness: 20 * time.Second,
			},
			{ // skipped: stale data
				UID: "ROSN-UID", FIGI: "BBG000ABC4", Ticker: "ROSN", ClassCode: "TQBR",
				Lot: 1, MinPriceIncrement: model.MustDecimal("0.2"),
				Quote: decision.QuoteView{
					Bid: model.MustDecimal("599"), Ask: model.MustDecimal("601"), Last: model.MustDecimal("600"), TS: asOf.Add(-2 * time.Hour),
				},
				Features: features.Snapshot{
					UID: "ROSN-UID", Interval: model.Interval5m, AsOf: asOf.Add(-2 * time.Hour),
					LastClose: model.MustDecimal("600"), Volume: 4200,
					SMA: 598, EMAFast: 601, EMASlow: 595, RSI: 62, ATR: 8,
					EMATrend: features.EMABullish, RSIZone: features.RSINeutral,
					Params: features.DefaultParams(),
				},
				DataFreshness: 2 * time.Hour,
			},
		},
		Limits: decision.Limits{
			MaxPositionNotional: model.MustDecimal("2000000"),
			MaxTotalExposure:    model.MustDecimal("5000000"),
			MaxOrdersPerCycle:   10,
			MaxOrdersPerDay:     50,
			MaxDailyLoss:        model.MustDecimal("20000"),
			CashFloor:           model.MustDecimal("10000"),
		},
	}
}

// TestGolden_RulesEngine locks the rules engine's output for a fixed,
// realistic Request. Run `go test ./internal/decision/rules/... -run
// TestGolden_RulesEngine -update` to regenerate both files after an
// intentional strategy change.
func TestGolden_RulesEngine(t *testing.T) {
	req := goldenRequest()

	gotReq, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	gotReq = append(gotReq, '\n')

	if *update {
		writeGolden(t, goldenRequestPath, gotReq)
	}
	wantReq := readGolden(t, goldenRequestPath)
	if string(gotReq) != string(wantReq) {
		t.Fatalf("testdata/golden_request.json is stale versus goldenRequest(); rerun with -update if this drift is intentional")
	}

	// Use a simulated clock so Meta.DurationMS is also deterministic,
	// though only Response is part of the golden comparison.
	eng, err := New(DefaultParams(), clock.NewSimulated(req.AsOf))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, _, err := eng.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	gotResp, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	gotResp = append(gotResp, '\n')

	if *update {
		writeGolden(t, goldenResponsePath, gotResp)
	}
	wantResp := readGolden(t, goldenResponsePath)
	if string(gotResp) != string(wantResp) {
		t.Fatalf("golden response mismatch (run with -update if this change is intentional):\n--- got ---\n%s\n--- want ---\n%s", gotResp, wantResp)
	}

	// The golden fixture doubles as a shape/semantics regression check.
	if errs := decision.ValidateShape(resp); len(errs) != 0 {
		t.Fatalf("ValidateShape errors on golden response: %+v", errs)
	}
	if errs := decision.ValidateSemantics(resp, req); len(errs) != 0 {
		t.Fatalf("ValidateSemantics errors on golden response: %+v", errs)
	}
}

func writeGolden(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run with -update to create it): %v", path, err)
	}
	return data
}
