// Package cycle is the autonomous decision loop that stitches the modules into a
// trading robot (DESIGN §6). A scheduler ticks on completed-candle boundaries,
// gated to the trading session, and runs one cycle at a time through the state
// machine: assemble → decide → validate → risk-check → execute → account →
// report. Every step persists to the store so a cycle is replayable.
//
// The loop reads strictly as-of the cycle's watermark: candles, quotes and
// feature snapshots come from the as-of store reads, so a cycle at time T never
// sees data after T and only completed candles. Data too stale for the
// configured ceiling makes the cycle skip and record why. Fills do not happen
// inside a cycle: the paper executor rests each intent and settles it on the
// next quote observation, driven by the market collector's quote listener.
//
// The Engine also implements the operator controls the TUI drives (pause,
// resume, kill switch, status, cancel) without importing the TUI: the app wires
// the concrete Engine to the TUI's consumer-owned interfaces. The kill switch
// latches a durable operational halt so risk blocks new buys until an operator
// clears it.
package cycle
