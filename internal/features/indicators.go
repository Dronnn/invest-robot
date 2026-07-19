package features

import (
	"fmt"
	"math"

	"github.com/Dronnn/invest-robot/internal/model"
)

// SMA returns the simple moving average of the closing prices of the last
// period candles (oldest→newest, complete bars only). It requires
// len(candles) >= period.
func SMA(candles []model.Candle, period int) (float64, error) {
	if period <= 0 {
		return 0, fmt.Errorf("features: SMA: period must be positive, got %d", period)
	}
	if len(candles) < period {
		return 0, ErrInsufficientData{Required: period, Got: len(candles)}
	}

	window := candles[len(candles)-period:]
	var sum float64
	for _, c := range window {
		sum += c.Close.Float64()
	}
	return sum / float64(period), nil
}

// EMA returns the exponential moving average of candles with standard
// seeding: the first value is the SMA of the first period closes, after
// which the standard multiplier k = 2/(period+1) is applied forward,
// candle by candle, through the rest of candles to the last one. It
// requires len(candles) >= period. Supplying more history than the minimum
// lets the EMA converge further past its seed before the value is read.
func EMA(candles []model.Candle, period int) (float64, error) {
	if period <= 0 {
		return 0, fmt.Errorf("features: EMA: period must be positive, got %d", period)
	}
	if len(candles) < period {
		return 0, ErrInsufficientData{Required: period, Got: len(candles)}
	}

	var seed float64
	for _, c := range candles[:period] {
		seed += c.Close.Float64()
	}
	seed /= float64(period)

	k := 2.0 / float64(period+1)
	ema := seed
	for _, c := range candles[period:] {
		ema = c.Close.Float64()*k + ema*(1-k)
	}
	return ema, nil
}

// RSI returns the Relative Strength Index of candles using Wilder smoothing
// (not the naive SMA-of-gains/losses variant): the first average gain and
// average loss are the plain average of the first period closing price
// changes, after which each subsequent change is folded in with Wilder's
// recursive average avg = (avg*(period-1) + current)/period, walked forward
// to the last candle. It requires len(candles) >= period+1, since period
// price changes need period+1 closes.
//
// By convention, a series with no price movement at all (average gain and
// average loss both zero) returns 50 (neutral); a series with movement but
// no losses returns 100.
func RSI(candles []model.Candle, period int) (float64, error) {
	if period <= 0 {
		return 0, fmt.Errorf("features: RSI: period must be positive, got %d", period)
	}
	required := period + 1
	if len(candles) < required {
		return 0, ErrInsufficientData{Required: required, Got: len(candles)}
	}

	var avgGain, avgLoss float64
	for i := 1; i <= period; i++ {
		change := candles[i].Close.Float64() - candles[i-1].Close.Float64()
		if change > 0 {
			avgGain += change
		} else {
			avgLoss += -change
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	for i := period + 1; i < len(candles); i++ {
		change := candles[i].Close.Float64() - candles[i-1].Close.Float64()
		var gain, loss float64
		if change > 0 {
			gain = change
		} else {
			loss = -change
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
	}

	if avgLoss == 0 {
		if avgGain == 0 {
			return 50, nil
		}
		return 100, nil
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs)), nil
}

// ATR returns the Average True Range of candles using Wilder smoothing: the
// true range of a bar is max(high-low, |high-prevClose|, |low-prevClose|);
// the first average is the plain average of the first period true ranges,
// after which each subsequent true range is folded in with Wilder's
// recursive average avg = (avg*(period-1) + current)/period, walked forward
// to the last candle. It requires len(candles) >= period+1, since period
// true ranges each need a previous close.
func ATR(candles []model.Candle, period int) (float64, error) {
	if period <= 0 {
		return 0, fmt.Errorf("features: ATR: period must be positive, got %d", period)
	}
	required := period + 1
	if len(candles) < required {
		return 0, ErrInsufficientData{Required: required, Got: len(candles)}
	}

	trueRange := func(i int) float64 {
		high := candles[i].High.Float64()
		low := candles[i].Low.Float64()
		prevClose := candles[i-1].Close.Float64()
		tr := high - low
		if hc := math.Abs(high - prevClose); hc > tr {
			tr = hc
		}
		if lc := math.Abs(low - prevClose); lc > tr {
			tr = lc
		}
		return tr
	}

	var avgTR float64
	for i := 1; i <= period; i++ {
		avgTR += trueRange(i)
	}
	avgTR /= float64(period)

	for i := period + 1; i < len(candles); i++ {
		avgTR = (avgTR*float64(period-1) + trueRange(i)) / float64(period)
	}
	return avgTR, nil
}
