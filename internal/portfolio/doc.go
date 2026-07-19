// Package portfolio is the single owner of cash, positions, fees, and PnL
// (DESIGN.md §3, §5, §6). It applies fills transactionally, marks the book
// to market, and answers the read-only questions the rest of the robot asks
// about account state: what is my cash, my equity, my positions, and my PnL
// today.
//
// Every method takes a sqlite.Querier rather than opening its own
// connection or transaction — callers that need a fill and its accounting
// effects to commit atomically drive sqlite.WithTx themselves and pass the
// resulting *sql.Tx through. Nothing in this package calls time.Now(); every
// method that needs "now" takes it from the injected clock.Clock or an
// explicit `at`/`sessionStart` parameter, per DESIGN.md §3's clock
// discipline.
package portfolio
