package features

import (
	"math"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

const epsilon = 1e-9

func closeEnough(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > epsilon {
		t.Errorf("got %v, want %v (diff %v)", got, want, math.Abs(got-want))
	}
}

// candle builds a minimal candle for indicator tests: only Close, High, Low,
// and TS matter to the functions under test. ts orders the series
// oldest→newest by minute offset from a fixed epoch.
func candle(minuteOffset int, open, high, low, close string) model.Candle {
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	return model.Candle{
		InstrumentUID: "TEST",
		Interval:      model.Interval1m,
		Open:          model.MustDecimal(open),
		High:          model.MustDecimal(high),
		Low:           model.MustDecimal(low),
		Close:         model.MustDecimal(close),
		Volume:        100,
		TS:            base.Add(time.Duration(minuteOffset) * time.Minute),
		Complete:      true,
	}
}

// closesOnly builds a candle series from closes alone, with High=Low=Close
// (used by SMA/EMA/RSI tests, which don't touch High/Low).
func closesOnly(closes ...string) []model.Candle {
	cs := make([]model.Candle, len(closes))
	for i, c := range closes {
		cs[i] = candle(i, c, c, c, c)
	}
	return cs
}

// --- SMA ---
//
// Reference: SMA(period) is the plain average of the last `period` closes.
// Series: 10, 20, 30, 40, 50.

func TestSMA(t *testing.T) {
	cs := closesOnly("10", "20", "30", "40", "50")

	t.Run("period 5 over exactly 5 candles", func(t *testing.T) {
		got, err := SMA(cs, 5)
		if err != nil {
			t.Fatalf("SMA: %v", err)
		}
		// (10+20+30+40+50)/5 = 30
		closeEnough(t, got, 30)
	})

	t.Run("period 3 uses only the trailing window", func(t *testing.T) {
		got, err := SMA(cs, 3)
		if err != nil {
			t.Fatalf("SMA: %v", err)
		}
		// last 3 closes: 30,40,50 -> 120/3 = 40
		closeEnough(t, got, 40)
	})

	t.Run("period 1 is the last close", func(t *testing.T) {
		got, err := SMA(cs, 1)
		if err != nil {
			t.Fatalf("SMA: %v", err)
		}
		closeEnough(t, got, 50)
	})
}

func TestSMA_WarmUpBoundary(t *testing.T) {
	cs := closesOnly("10", "20", "30", "40", "50")

	if _, err := SMA(cs[:4], 5); err == nil {
		t.Fatal("expected ErrInsufficientData with 4 candles for period 5")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 5 || ins.Got != 4 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:5, Got:4}", err)
	}

	if _, err := SMA(cs, 5); err != nil {
		t.Fatalf("SMA at exact warm-up boundary: %v", err)
	}
}

func TestSMA_InvalidPeriod(t *testing.T) {
	cs := closesOnly("10")
	if _, err := SMA(cs, 0); err == nil {
		t.Fatal("expected error for period 0")
	}
	if _, err := SMA(cs, -1); err == nil {
		t.Fatal("expected error for negative period")
	}
}

func TestSMA_EmptyCandles(t *testing.T) {
	if _, err := SMA(nil, 1); err == nil {
		t.Fatal("expected ErrInsufficientData for empty candles")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 1 || ins.Got != 0 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:1, Got:0}", err)
	}
}

// --- EMA ---
//
// Reference: EMA(period) seeds on the SMA of the first `period` closes,
// then walks forward with k = 2/(period+1).
// Series: 10, 20, 30, 40, 50, 60; period 3.
//   seed = (10+20+30)/3 = 20
//   k = 2/4 = 0.5
//   candle 40: ema = 40*0.5 + 20*0.5 = 30
//   candle 50: ema = 50*0.5 + 30*0.5 = 40
//   candle 60: ema = 60*0.5 + 40*0.5 = 50

func TestEMA(t *testing.T) {
	cs := closesOnly("10", "20", "30", "40", "50", "60")

	got, err := EMA(cs, 3)
	if err != nil {
		t.Fatalf("EMA: %v", err)
	}
	closeEnough(t, got, 50)
}

func TestEMA_SeedOnly(t *testing.T) {
	// Exactly `period` candles: EMA is just the seed SMA.
	cs := closesOnly("10", "20", "30")
	got, err := EMA(cs, 3)
	if err != nil {
		t.Fatalf("EMA: %v", err)
	}
	closeEnough(t, got, 20)
}

func TestEMA_SingleCandlePeriod1(t *testing.T) {
	// period 1: k = 2/2 = 1, so EMA tracks the close exactly at every step.
	cs := closesOnly("10", "20", "30")
	got, err := EMA(cs, 1)
	if err != nil {
		t.Fatalf("EMA: %v", err)
	}
	closeEnough(t, got, 30)
}

func TestEMA_WarmUpBoundary(t *testing.T) {
	cs := closesOnly("10", "20", "30")

	if _, err := EMA(cs[:2], 3); err == nil {
		t.Fatal("expected ErrInsufficientData with 2 candles for period 3")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 3 || ins.Got != 2 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:3, Got:2}", err)
	}

	if _, err := EMA(cs, 3); err != nil {
		t.Fatalf("EMA at exact warm-up boundary: %v", err)
	}
}

func TestEMA_InvalidPeriod(t *testing.T) {
	cs := closesOnly("10")
	if _, err := EMA(cs, 0); err == nil {
		t.Fatal("expected error for period 0")
	}
}

// --- RSI (Wilder smoothing) ---
//
// Reference calculation, period 4, closes: 44, 44.25, 44.5, 43.75, 44.65
//   changes: +0.25, +0.25, -0.75, +0.90
//   avgGain seed = (0.25+0.25+0+0.90)/4 = 0.35
//   avgLoss seed = (0+0+0.75+0)/4 = 0.1875
//   RS = 0.35/0.1875 = 1.8666666667
//   RSI = 100 - 100/(1+RS) = 65.1162790698 (5 candles = exact warm-up
//   boundary for period 4, so no Wilder recursive step runs yet)
//
// Extending with a 6th close, 44.30 (change -0.35 from 44.65), exercises one
// Wilder recursive step beyond the seed:
//   avgGain = (0.35*3 + 0)/4 = 0.2625
//   avgLoss = (0.1875*3 + 0.35)/4 = 0.228125
//   RS = 0.2625/0.228125 = 84/73 = 1.1506849315
//   RSI = 100 - 100/(1+RS) = 53.5031847134

func TestRSI_SeedOnly(t *testing.T) {
	cs := closesOnly("44", "44.25", "44.5", "43.75", "44.65")
	got, err := RSI(cs, 4)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	closeEnough(t, got, 65.11627906976745)
}

func TestRSI_OneWilderStep(t *testing.T) {
	cs := closesOnly("44", "44.25", "44.5", "43.75", "44.65", "44.30")
	got, err := RSI(cs, 4)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	closeEnough(t, got, 53.50318471337573)
}

func TestRSI_WarmUpBoundary(t *testing.T) {
	cs := closesOnly("44", "44.25", "44.5", "43.75", "44.65")

	// period 4 needs period+1 = 5 closes.
	if _, err := RSI(cs[:4], 4); err == nil {
		t.Fatal("expected ErrInsufficientData with 4 candles for period 4")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 5 || ins.Got != 4 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:5, Got:4}", err)
	}

	if _, err := RSI(cs, 4); err != nil {
		t.Fatalf("RSI at exact warm-up boundary: %v", err)
	}
}

func TestRSI_InvalidPeriod(t *testing.T) {
	cs := closesOnly("10", "20")
	if _, err := RSI(cs, 0); err == nil {
		t.Fatal("expected error for period 0")
	}
}

func TestRSI_EdgeAllGains(t *testing.T) {
	// Strictly rising series: avgLoss stays 0 throughout -> RSI == 100.
	cs := closesOnly("10", "11", "12", "13")
	got, err := RSI(cs, 3)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	closeEnough(t, got, 100)
}

func TestRSI_EdgeNoMovement(t *testing.T) {
	// Flat series: avgGain == avgLoss == 0 -> RSI defined as neutral (50).
	cs := closesOnly("10", "10", "10", "10")
	got, err := RSI(cs, 3)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	closeEnough(t, got, 50)
}

func TestRSI_EdgeAllLosses(t *testing.T) {
	cs := closesOnly("13", "12", "11", "10")
	got, err := RSI(cs, 3)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	closeEnough(t, got, 0)
}

// --- ATR (Wilder smoothing) ---
//
// Reference calculation, period 3. Candles (High, Low, Close), prevClose is
// the previous candle's Close:
//   c0: close=100 (prevClose source only)
//   c1: H=105 L=98  C=102  TR1 = max(7, |105-100|=5, |98-100|=2)  = 7
//   c2: H=103 L=99  C=101  TR2 = max(4, |103-102|=1, |99-102|=3)  = 4
//   c3: H=108 L=100 C=107  TR3 = max(8, |108-101|=7, |100-101|=1) = 8
//   seed avgTR = (7+4+8)/3 = 19/3 = 6.333333333 (4 candles = exact warm-up
//   boundary for period 3)
//
// Extending with c4 (H=110 L=104 C=106, prevClose=107):
//   TR4 = max(6, |110-107|=3, |104-107|=3) = 6
//   avgTR = (19/3*2 + 6)/3 = 56/9 = 6.222222222

func atrTestCandles() []model.Candle {
	return []model.Candle{
		candle(0, "100", "100", "100", "100"),
		candle(1, "100", "105", "98", "102"),
		candle(2, "101", "103", "99", "101"),
		candle(3, "102", "108", "100", "107"),
		candle(4, "107", "110", "104", "106"),
	}
}

func TestATR_SeedOnly(t *testing.T) {
	cs := atrTestCandles()[:4]
	got, err := ATR(cs, 3)
	if err != nil {
		t.Fatalf("ATR: %v", err)
	}
	closeEnough(t, got, 19.0/3.0)
}

func TestATR_OneWilderStep(t *testing.T) {
	cs := atrTestCandles()
	got, err := ATR(cs, 3)
	if err != nil {
		t.Fatalf("ATR: %v", err)
	}
	closeEnough(t, got, 56.0/9.0)
}

func TestATR_WarmUpBoundary(t *testing.T) {
	cs := atrTestCandles()[:4]

	// period 3 needs period+1 = 4 candles.
	if _, err := ATR(cs[:3], 3); err == nil {
		t.Fatal("expected ErrInsufficientData with 3 candles for period 3")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 4 || ins.Got != 3 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:4, Got:3}", err)
	}

	if _, err := ATR(cs, 3); err != nil {
		t.Fatalf("ATR at exact warm-up boundary: %v", err)
	}
}

func TestATR_InvalidPeriod(t *testing.T) {
	cs := atrTestCandles()
	if _, err := ATR(cs, 0); err == nil {
		t.Fatal("expected error for period 0")
	}
}

func TestATR_EmptyAndSingleCandle(t *testing.T) {
	if _, err := ATR(nil, 3); err == nil {
		t.Fatal("expected ErrInsufficientData for empty candles")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 4 || ins.Got != 0 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:4, Got:0}", err)
	}

	single := atrTestCandles()[:1]
	if _, err := ATR(single, 3); err == nil {
		t.Fatal("expected ErrInsufficientData for a single candle")
	} else if ins, ok := err.(ErrInsufficientData); !ok || ins.Required != 4 || ins.Got != 1 {
		t.Fatalf("got %#v, want ErrInsufficientData{Required:4, Got:1}", err)
	}
}
