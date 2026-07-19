package model

// InstrumentUID is T-Bank's stable instrument identifier (the primary key the
// robot uses everywhere).
type InstrumentUID string

// FIGI is the Financial Instrument Global Identifier, an alternate key some
// tinvest commands accept.
type FIGI string

// InstrumentRef is the set of identifiers that name an instrument without
// carrying its trading parameters. It is embedded in richer types and used
// where only identity is needed.
type InstrumentRef struct {
	UID       InstrumentUID
	FIGI      FIGI
	Ticker    string
	ClassCode string
}

// Instrument is a tradable instrument with the parameters the robot needs to
// size and price orders.
type Instrument struct {
	InstrumentRef
	Lot               int64   // shares per lot; order quantities are in lots
	MinPriceIncrement Decimal // price tick; limit prices must align to it
	Currency          string
	Name              string
}
