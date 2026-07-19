// Package features computes technical indicators and assembles feature
// snapshots from historical candle data.
//
// Every function here is pure: the same input always produces the same
// output, there is no I/O, and the only dependencies are the standard
// library and internal/model (see DESIGN.md §3). That purity rests on a
// contract every function in this package shares:
//
//   - candles are ordered oldest→newest;
//   - candles contains only completed bars — the caller (the market
//     collector and cycle assembly step, per DESIGN.md §6's as-of
//     discipline) is responsible for excluding the still-forming current
//     candle and for not passing anything with event_time after the
//     cycle's as-of watermark.
//
// This package does not inspect model.Candle.Complete or candle timestamps
// to enforce the contract; a caller that violates it gets a silently wrong
// indicator value, not an error.
//
// Indicator math is done in float64 (money stays in model.Decimal
// everywhere else in the system; see DESIGN.md §2). Results are therefore
// approximations suitable for signals and sizing, never for ledger
// bookkeeping.
package features
