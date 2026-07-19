// Package execution turns risk-approved decisions into durable order intents
// and drives them to a terminal state. It owns the order-intent journal (the
// DESIGN §4 mutation discipline: a stable client-order-id record is persisted
// before any submission or fill) and the fill records; the position, cash and
// PnL effects of a fill belong to internal/portfolio, which execution reaches
// only through the consumer-owned FillApplier interface.
//
// The Executor port (Submit/OnQuote) is implemented by internal/execution/paper
// for paper trading and backtests, and later by internal/execution/live for
// real orders. Everything below the orchestration layer takes a clock.Clock and
// never calls time.Now, so a backtest replay over the same script is
// deterministic.
package execution
