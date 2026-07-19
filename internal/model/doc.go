// Package model holds the shared domain types of the robot: fixed-point
// Decimal money/price/quantity values, instrument identifiers, market data
// (candles, quotes), the order/intent domain with its state machine,
// positions, and the decision core. It is a pure package — standard library
// only (math/big for arithmetic intermediates), no I/O — so every layer above
// can depend on it without pulling in infrastructure.
//
// Time invariant: every time.Time field on a model type is in UTC. Construct
// values through UTC at boundaries to keep that invariant, and rely on it when
// comparing or persisting timestamps.
package model

import "time"

// UTC returns t in the UTC location. All time.Time fields on model types are
// expected to be UTC; call this where a timestamp enters the model from an
// external source whose location is not guaranteed.
func UTC(t time.Time) time.Time { return t.UTC() }
