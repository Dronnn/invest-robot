package features

import "fmt"

// ErrInsufficientData is returned by an indicator function or Build when
// fewer complete candles were supplied than are needed to produce a value
// without half-warming it. Required is the minimum candle count the caller
// needed to supply; Got is how many were actually supplied.
type ErrInsufficientData struct {
	Required int
	Got      int
}

func (e ErrInsufficientData) Error() string {
	return fmt.Sprintf("features: insufficient data: need at least %d candles, got %d", e.Required, e.Got)
}
