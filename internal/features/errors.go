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

// ErrCandleMismatch is returned by Build when a candle does not belong to the
// instrument/interval the snapshot is being built for. Index is the offending
// candle's position; Field is "instrument_uid" or "interval"; Want and Got are
// the requested and actual values.
type ErrCandleMismatch struct {
	Index int
	Field string
	Want  string
	Got   string
}

func (e ErrCandleMismatch) Error() string {
	return fmt.Sprintf("features: candle %d %s = %q, want %q", e.Index, e.Field, e.Got, e.Want)
}
