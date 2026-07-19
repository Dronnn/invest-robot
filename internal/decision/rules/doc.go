// Package rules implements decision.Engine as a deterministic, indicator-
// based trend/momentum strategy: long-only, ATR-sized, no I/O beyond the
// clock used for its own duration metric. It is the Phase 1 engine and the
// engine backtests run against (DESIGN.md §8, §14).
//
// Decide is a pure function of its Request: the same Request always
// produces byte-identical JSON output (see golden_test.go), which is what
// makes strategy tuning and backtest replay trustworthy.
package rules
