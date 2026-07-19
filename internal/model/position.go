package model

import "time"

// Position is the robot's holding in one instrument. Qty is in lots; it is
// non-negative in Phase 1 (shorts are out of scope) but the type can represent
// a negative quantity. AvgPrice is the average entry price per share.
type Position struct {
	InstrumentUID InstrumentUID
	Qty           int64
	AvgPrice      Decimal
	UpdatedAt     time.Time
}
