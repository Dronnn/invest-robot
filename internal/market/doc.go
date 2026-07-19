// Package market collects market data through the tinvest CLI seam and persists
// it for the decision cycle to read.
//
// The Collector resolves the configured universe once at startup (a fail-closed
// step: an unknown instrument aborts start), backfills authoritative completed
// candles per instrument via unary CandlesGet, then supervises a single
// marketdata stream for freshness — forming bars and last-price quotes. Streamed
// forming bars are written incomplete; when a bar rolls over, the just-closed
// bar is confirmed with a small unary fetch so the stored complete row is
// authoritative (never the partial stream frame). Stream gaps and restarts
// trigger a unary backfill of the missed range.
//
// Data problems never crash the service (DESIGN §12): they are logged to the
// event store, the affected instrument is marked stale, and collection
// continues. Health reports per-instrument freshness and stream state for the
// decision request and the TUI. All time comes from an injected clock so replay
// stays honest.
//
// The store and broker dependencies are small interfaces owned here (Broker,
// InstrumentSink, CandleStore, QuoteSink, EventLog); a sqlite-backed adapter and
// a tinvest-client adapter are provided for wiring, but the collector core
// depends only on the interfaces.
package market
