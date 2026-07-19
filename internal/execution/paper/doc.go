// Package paper is the paper-trading fill simulator: an Executor that journals
// order intents and simulates their fills against a quote stream, without any
// order ever leaving the machine. It implements the DESIGN §7 fill model —
// adverse slippage on market orders, marketable-limit crossing, tick alignment,
// last-price fallback, quote-freshness and session gating, commission, and the
// next-observation discipline (a decision never fills inside the bar it was made
// on).
//
// The same simulator serves live paper trading (real quotes feed OnQuote) and
// backtests (historical quotes feed OnQuote); only the event source and the
// clock differ. All time comes from a clock.Clock, so replaying the same script
// of Submit/OnQuote/ExpireDay calls produces byte-identical fills.
package paper
