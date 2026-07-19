package features

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// seriesCandles builds n candles (oldest→newest) with Close = start +
// step*i and a fixed +-1 High/Low band, so ATR has a non-degenerate true
// range at every bar. Used where Build's own composition is under test, not
// a specific indicator's arithmetic (that's covered in indicators_test.go).
func seriesCandles(n int, start, step float64) []model.Candle {
	cs := make([]model.Candle, n)
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		c := start + step*float64(i)
		cs[i] = model.Candle{
			InstrumentUID: "TEST",
			Interval:      model.Interval5m,
			Open:          model.MustDecimal(ftoa(c)),
			High:          model.MustDecimal(ftoa(c + 1)),
			Low:           model.MustDecimal(ftoa(c - 1)),
			Close:         model.MustDecimal(ftoa(c)),
			Volume:        int64(1000 + i),
			TS:            base.Add(time.Duration(i*5) * time.Minute),
			Complete:      true,
		}
	}
	return cs
}

// ftoa renders a float as a fixed-point string acceptable to
// model.ParseDecimal (no exponent notation, which %v/%g can produce for
// large/small magnitudes).
func ftoa(f float64) string {
	return fmt.Sprintf("%.3f", f)
}

func smallParams() Params {
	return Params{
		SMAPeriod:     2,
		EMAFastPeriod: 2,
		EMASlowPeriod: 3,
		RSIPeriod:     3,
		ATRPeriod:     2,
	}
}

func TestBuild_ComposesIndicators(t *testing.T) {
	cs := seriesCandles(10, 100, 1) // rising series
	params := smallParams()

	got, err := Build("SBER-UID", model.Interval5m, cs, params)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantSMA, err := SMA(cs, params.SMAPeriod)
	if err != nil {
		t.Fatalf("SMA: %v", err)
	}
	wantEMAFast, err := EMA(cs, params.EMAFastPeriod)
	if err != nil {
		t.Fatalf("EMA fast: %v", err)
	}
	wantEMASlow, err := EMA(cs, params.EMASlowPeriod)
	if err != nil {
		t.Fatalf("EMA slow: %v", err)
	}
	wantRSI, err := RSI(cs, params.RSIPeriod)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	wantATR, err := ATR(cs, params.ATRPeriod)
	if err != nil {
		t.Fatalf("ATR: %v", err)
	}

	closeEnough(t, got.SMA, wantSMA)
	closeEnough(t, got.EMAFast, wantEMAFast)
	closeEnough(t, got.EMASlow, wantEMASlow)
	closeEnough(t, got.RSI, wantRSI)
	closeEnough(t, got.ATR, wantATR)

	last := cs[len(cs)-1]
	if got.UID != "SBER-UID" {
		t.Errorf("UID = %q, want SBER-UID", got.UID)
	}
	if got.Interval != model.Interval5m {
		t.Errorf("Interval = %v, want %v", got.Interval, model.Interval5m)
	}
	if !got.AsOf.Equal(last.TS) {
		t.Errorf("AsOf = %v, want %v", got.AsOf, last.TS)
	}
	if got.LastClose.Cmp(last.Close) != 0 {
		t.Errorf("LastClose = %v, want %v", got.LastClose, last.Close)
	}
	if got.Volume != last.Volume {
		t.Errorf("Volume = %v, want %v", got.Volume, last.Volume)
	}
	if got.Params != params {
		t.Errorf("Params = %+v, want %+v", got.Params, params)
	}
}

func TestBuild_EMATrend(t *testing.T) {
	params := smallParams()

	t.Run("bullish on a rising series", func(t *testing.T) {
		snap, err := Build("UID", model.Interval5m, seriesCandles(10, 100, 2), params)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if snap.EMATrend != EMABullish {
			t.Errorf("EMATrend = %v, want %v (fast=%v slow=%v)", snap.EMATrend, EMABullish, snap.EMAFast, snap.EMASlow)
		}
	})

	t.Run("bearish on a falling series", func(t *testing.T) {
		snap, err := Build("UID", model.Interval5m, seriesCandles(10, 100, -2), params)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if snap.EMATrend != EMABearish {
			t.Errorf("EMATrend = %v, want %v (fast=%v slow=%v)", snap.EMATrend, EMABearish, snap.EMAFast, snap.EMASlow)
		}
	})

	t.Run("flat on a constant series", func(t *testing.T) {
		snap, err := Build("UID", model.Interval5m, seriesCandles(10, 100, 0), params)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if snap.EMATrend != EMAFlat {
			t.Errorf("EMATrend = %v, want %v (fast=%v slow=%v)", snap.EMATrend, EMAFlat, snap.EMAFast, snap.EMASlow)
		}
	})
}

func TestBuild_RSIZone(t *testing.T) {
	params := smallParams()

	t.Run("overbought on a strongly rising series", func(t *testing.T) {
		snap, err := Build("UID", model.Interval5m, seriesCandles(10, 100, 5), params)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if snap.RSIZone != RSIOverbought {
			t.Errorf("RSIZone = %v, want %v (rsi=%v)", snap.RSIZone, RSIOverbought, snap.RSI)
		}
	})

	t.Run("oversold on a strongly falling series", func(t *testing.T) {
		snap, err := Build("UID", model.Interval5m, seriesCandles(10, 100, -5), params)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if snap.RSIZone != RSIOversold {
			t.Errorf("RSIZone = %v, want %v (rsi=%v)", snap.RSIZone, RSIOversold, snap.RSI)
		}
	})

	t.Run("neutral on a flat series", func(t *testing.T) {
		snap, err := Build("UID", model.Interval5m, seriesCandles(10, 100, 0), params)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if snap.RSIZone != RSINeutral {
			t.Errorf("RSIZone = %v, want %v (rsi=%v)", snap.RSIZone, RSINeutral, snap.RSI)
		}
	})
}

func TestBuild_DefaultParamsWarmUp(t *testing.T) {
	// DefaultParams' binding constraint is EMASlowPeriod = 50.
	cs := seriesCandles(50, 100, 1)

	if _, err := Build("UID", model.Interval5m, cs[:49], DefaultParams()); err == nil {
		t.Fatal("expected ErrInsufficientData with 49 candles")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 50 || ins.Got != 49 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:50, Got:49}", err)
	}

	if _, err := Build("UID", model.Interval5m, cs, DefaultParams()); err != nil {
		t.Fatalf("Build at exact warm-up boundary: %v", err)
	}
}

func TestBuild_InsufficientData_EmptyAndSingle(t *testing.T) {
	// smallParams' binding constraint is RSIPeriod+1 = 4 (SMA/EMA need at
	// most 3, ATR needs 3).
	if _, err := Build("UID", model.Interval5m, nil, smallParams()); err == nil {
		t.Fatal("expected ErrInsufficientData for nil candles")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 4 || ins.Got != 0 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:4, Got:0}", err)
	}

	single := seriesCandles(1, 100, 0)
	if _, err := Build("UID", model.Interval5m, single, smallParams()); err == nil {
		t.Fatal("expected ErrInsufficientData for a single candle")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 4 || ins.Got != 1 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:4, Got:1}", err)
	}
}

func TestBuild_InvalidParams(t *testing.T) {
	cs := seriesCandles(10, 100, 1)
	bad := smallParams()
	bad.RSIPeriod = 0

	_, err := Build("UID", model.Interval5m, cs, bad)
	if err == nil {
		t.Fatal("expected an error for a non-positive period")
	}
	if _, ok := err.(ErrInsufficientData); ok {
		t.Fatal("invalid params must not be reported as ErrInsufficientData")
	}
}

func TestParams_RequiredCandles(t *testing.T) {
	p := Params{SMAPeriod: 5, EMAFastPeriod: 3, EMASlowPeriod: 10, RSIPeriod: 6, ATRPeriod: 4}
	// max(5, 3, 10, 6+1=7, 4+1=5) = 10
	if got := p.requiredCandles(); got != 10 {
		t.Errorf("requiredCandles() = %d, want 10", got)
	}
}

func TestParams_Validate(t *testing.T) {
	base := DefaultParams()

	cases := []struct {
		name   string
		mutate func(p *Params)
	}{
		{"sma_period", func(p *Params) { p.SMAPeriod = 0 }},
		{"ema_fast_period", func(p *Params) { p.EMAFastPeriod = 0 }},
		{"ema_slow_period", func(p *Params) { p.EMASlowPeriod = -1 }},
		{"rsi_period", func(p *Params) { p.RSIPeriod = 0 }},
		{"atr_period", func(p *Params) { p.ATRPeriod = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			if err := p.validate(); err == nil {
				t.Fatalf("expected an error for invalid %s", tc.name)
			}
		})
	}

	if err := base.validate(); err != nil {
		t.Fatalf("DefaultParams() should validate cleanly: %v", err)
	}
}

func TestBuild_Determinism(t *testing.T) {
	cs := seriesCandles(10, 100, 1.5)
	params := smallParams()

	snap1, err := Build("SBER-UID", model.Interval5m, cs, params)
	if err != nil {
		t.Fatalf("Build (1): %v", err)
	}
	snap2, err := Build("SBER-UID", model.Interval5m, cs, params)
	if err != nil {
		t.Fatalf("Build (2): %v", err)
	}

	if snap1 != snap2 {
		t.Fatalf("Build is not deterministic: %+v != %+v", snap1, snap2)
	}

	json1, err := json.Marshal(snap1)
	if err != nil {
		t.Fatalf("marshal (1): %v", err)
	}
	json2, err := json.Marshal(snap2)
	if err != nil {
		t.Fatalf("marshal (2): %v", err)
	}
	if string(json1) != string(json2) {
		t.Fatalf("Snapshot JSON is not deterministic:\n%s\nvs\n%s", json1, json2)
	}
}

func TestSnapshot_JSONShape(t *testing.T) {
	cs := seriesCandles(10, 100, 1)
	snap, err := Build("SBER-UID", model.Interval5m, cs, smallParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal into map: %v", err)
	}

	wantKeys := []string{
		"uid", "interval", "as_of", "last_close", "volume",
		"sma", "ema_fast", "ema_slow", "rsi", "atr",
		"ema_trend", "rsi_zone", "params",
	}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON payload missing key %q; got %s", k, raw)
		}
	}

	// last_close must be a decimal string, not a bare JSON number.
	var lastClose string
	if err := json.Unmarshal(m["last_close"], &lastClose); err != nil {
		t.Errorf("last_close is not a JSON string: %s", m["last_close"])
	}

	// sma must be a plain JSON number, not a string.
	var sma float64
	if err := json.Unmarshal(m["sma"], &sma); err != nil {
		t.Errorf("sma is not a JSON number: %s", m["sma"])
	}

	// Round-trip check: unmarshaling back into a Snapshot reproduces the
	// original value.
	var roundTripped Snapshot
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if roundTripped != snap {
		t.Fatalf("round trip mismatch:\n%+v\nvs\n%+v", roundTripped, snap)
	}
}
