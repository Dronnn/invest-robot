package decision

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/model"
)

func sampleRequest() Request {
	asOf := time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC)
	limitPrice := model.MustDecimal("250.50")
	fillPrice := model.MustDecimal("249.90")
	pnl := model.MustDecimal("15.25")

	return Request{
		AsOf: asOf,
		Mode: "paper",
		Cycle: CycleMeta{
			ID:        7,
			StartedAt: asOf,
			Interval:  "5m",
		},
		Portfolio: Portfolio{
			Cash:   model.MustDecimal("50000"),
			Equity: model.MustDecimal("100000"),
			Positions: []PositionView{
				{
					UID:           "GAZP-UID",
					Ticker:        "GAZP",
					Qty:           10,
					AvgPrice:      model.MustDecimal("150"),
					LastPrice:     model.MustDecimal("140"),
					UnrealizedPnL: model.MustDecimal("-100"),
				},
			},
		},
		OpenIntents: []IntentView{
			{
				ClientOrderID: "11111111-1111-1111-1111-111111111111",
				InstrumentUID: "GAZP-UID",
				Side:          model.SideBuy,
				Qty:           5,
				Type:          model.OrderLimit,
				LimitPrice:    &limitPrice,
				TimeInForce:   model.TIFDay,
				State:         model.IntentSubmitted,
				CreatedAt:     asOf,
			},
		},
		Instruments: []InstrumentContext{
			{
				UID:               "SBER-UID",
				FIGI:              "BBG1",
				Ticker:            "SBER",
				ClassCode:         "TQBR",
				Lot:               10,
				MinPriceIncrement: model.MustDecimal("0.01"),
				Quote: QuoteView{
					Bid:  model.MustDecimal("249.90"),
					Ask:  model.MustDecimal("250.10"),
					Last: model.MustDecimal("250"),
					TS:   asOf,
				},
				Features: features.Snapshot{
					UID:       "SBER-UID",
					Interval:  model.Interval5m,
					AsOf:      asOf,
					LastClose: model.MustDecimal("250"),
					Volume:    10000,
					SMA:       248,
					EMAFast:   251,
					EMASlow:   245,
					RSI:       60,
					ATR:       2.5,
					EMATrend:  features.EMABullish,
					RSIZone:   features.RSINeutral,
					Params:    features.DefaultParams(),
				},
				DataFreshness: 30 * time.Second,
			},
		},
		Limits: Limits{
			MaxPositionNotional: model.MustDecimal("1000000"),
			MaxTotalExposure:    model.MustDecimal("5000000"),
			MaxOrdersPerCycle:   10,
			MaxOrdersPerDay:     50,
			MaxDailyLoss:        model.MustDecimal("20000"),
			Allowlist:           []string{"SBER-UID", "GAZP-UID"},
			CashFloor:           model.MustDecimal("1000"),
		},
		RecentOutcomes: []OutcomeView{
			{
				CycleID:       6,
				AsOf:          asOf.Add(-5 * time.Minute),
				InstrumentUID: "GAZP-UID",
				Action:        model.ActionBuy,
				Qty:           10,
				FillPrice:     &fillPrice,
				RealizedPnL:   &pnl,
				Outcome:       "filled",
			},
		},
	}
}

func TestRequest_JSONRoundTrip(t *testing.T) {
	req := sampleRequest()

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Request
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	raw2, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal (2): %v", err)
	}
	if string(raw) != string(raw2) {
		t.Fatalf("round trip is not byte-identical:\n%s\nvs\n%s", raw, raw2)
	}
}

func TestRequest_JSONFieldNamesAreSnakeCase(t *testing.T) {
	req := sampleRequest()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wantTopKeys := []string{
		"as_of", "mode", "cycle", "portfolio", "open_intents",
		"instruments", "limits", "recent_outcomes",
	}
	for _, k := range wantTopKeys {
		if _, ok := top[k]; !ok {
			t.Errorf("Request JSON missing key %q; got %s", k, raw)
		}
	}

	var cycle map[string]json.RawMessage
	if err := json.Unmarshal(top["cycle"], &cycle); err != nil {
		t.Fatalf("unmarshal cycle: %v", err)
	}
	for _, k := range []string{"id", "started_at", "interval"} {
		if _, ok := cycle[k]; !ok {
			t.Errorf("CycleMeta JSON missing key %q", k)
		}
	}

	var portfolio map[string]json.RawMessage
	if err := json.Unmarshal(top["portfolio"], &portfolio); err != nil {
		t.Fatalf("unmarshal portfolio: %v", err)
	}
	for _, k := range []string{"cash", "equity", "positions"} {
		if _, ok := portfolio[k]; !ok {
			t.Errorf("Portfolio JSON missing key %q", k)
		}
	}

	var positions []map[string]json.RawMessage
	if err := json.Unmarshal(portfolio["positions"], &positions); err != nil {
		t.Fatalf("unmarshal positions: %v", err)
	}
	for _, k := range []string{"uid", "ticker", "qty", "avg_price", "last_price", "unrealized_pnl"} {
		if _, ok := positions[0][k]; !ok {
			t.Errorf("PositionView JSON missing key %q", k)
		}
	}

	var instruments []map[string]json.RawMessage
	if err := json.Unmarshal(top["instruments"], &instruments); err != nil {
		t.Fatalf("unmarshal instruments: %v", err)
	}
	for _, k := range []string{"uid", "figi", "ticker", "class_code", "lot", "min_price_increment", "quote", "features", "data_freshness_ns"} {
		if _, ok := instruments[0][k]; !ok {
			t.Errorf("InstrumentContext JSON missing key %q", k)
		}
	}

	var quote map[string]json.RawMessage
	if err := json.Unmarshal(instruments[0]["quote"], &quote); err != nil {
		t.Fatalf("unmarshal quote: %v", err)
	}
	for _, k := range []string{"bid", "ask", "last", "ts"} {
		if _, ok := quote[k]; !ok {
			t.Errorf("QuoteView JSON missing key %q", k)
		}
	}

	var limits map[string]json.RawMessage
	if err := json.Unmarshal(top["limits"], &limits); err != nil {
		t.Fatalf("unmarshal limits: %v", err)
	}
	for _, k := range []string{"max_position_notional", "max_total_exposure", "max_orders_per_cycle", "max_orders_per_day", "max_daily_loss", "allowlist", "cash_floor"} {
		if _, ok := limits[k]; !ok {
			t.Errorf("Limits JSON missing key %q", k)
		}
	}
}

func TestRequest_AsOfIsUTC(t *testing.T) {
	req := sampleRequest()
	if req.AsOf.Location() != time.UTC {
		t.Fatalf("AsOf location = %v, want UTC", req.AsOf.Location())
	}
}
