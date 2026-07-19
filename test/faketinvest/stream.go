package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// defaultStreamTime is used for generated frames (a signal-driven shutdown)
// when the scenario pins no explicit timestamp, keeping output deterministic.
const defaultStreamTime = "2026-07-19T10:00:00Z"

// candleIntervalNames maps the short --candles flag form to the proto enum
// name carried in a streamed candle frame's data.interval.
//
// The streamed Candle message's Interval field is typed
// investapi.SubscriptionInterval (internal/render/stream_views.go:
// value.GetInterval().String()), so its names are "SUBSCRIPTION_INTERVAL_*".
// This is a different enum family from the unary candles-get "CANDLE_INTERVAL_*"
// request/response — the two must not be conflated. The values below match the
// generated proto (internal/pb/investapi/marketdata.pb.go, SubscriptionInterval_name)
// exactly, and the robot's internal/tinvestcli stream adapter parses the same
// family.
var candleIntervalNames = map[string]string{
	"1m": "SUBSCRIPTION_INTERVAL_ONE_MINUTE", "2m": "SUBSCRIPTION_INTERVAL_2_MIN", "3m": "SUBSCRIPTION_INTERVAL_3_MIN",
	"5m": "SUBSCRIPTION_INTERVAL_FIVE_MINUTES", "10m": "SUBSCRIPTION_INTERVAL_10_MIN", "15m": "SUBSCRIPTION_INTERVAL_FIFTEEN_MINUTES",
	"30m": "SUBSCRIPTION_INTERVAL_30_MIN", "1h": "SUBSCRIPTION_INTERVAL_ONE_HOUR", "2h": "SUBSCRIPTION_INTERVAL_2_HOUR",
	"4h": "SUBSCRIPTION_INTERVAL_4_HOUR", "1d": "SUBSCRIPTION_INTERVAL_ONE_DAY", "1w": "SUBSCRIPTION_INTERVAL_WEEK",
	"1M": "SUBSCRIPTION_INTERVAL_MONTH",
}

// validOrderbookDepths are the depths the real CLI accepts for --orderbook,
// mirroring the sibling tinvest repo's internal/broker/marketdata.ValidDepths.
var validOrderbookDepths = map[int32]bool{1: true, 10: true, 20: true, 30: true, 40: true, 50: true}

// marketDataSubscription is a parsed, validated `stream marketdata` request.
type marketDataSubscription struct {
	instruments    []string // requested ids, deduped, in order (raw: uid, FIGI, or TICKER@CLASSCODE)
	candleInterval string   // short form ("5m"); "" if candles were not requested
	candleEnum     string   // candleIntervalNames[candleInterval]; "" if not requested
	orderbookDepth int32    // 0 if orderbook was not requested
	trades         bool
	lastPrice      bool
	info           bool
}

// parseMarketDataSubscription validates `stream marketdata`'s subscription
// flags, mirroring the sibling tinvest repo's
// internal/broker/streaming.MarketDataSubscriptions plus the syntax check
// cmd/tinvest/stream.go runs via validateInstrumentIDs — in that order: id
// count, then selected kinds, then per-kind format (interval, depth), then id
// syntax. All failures are USAGE (exit 2), matching the real CLI: none of
// this reaches the network.
func parseMarketDataSubscription(p parsedArgs) (marketDataSubscription, *errorBody) {
	ids := dedupeStrings(p.flagAll("--instrument"))
	if len(ids) == 0 {
		return marketDataSubscription{}, &errorBody{Code: "USAGE", Message: "at least one --instrument is required"}
	}

	sub := marketDataSubscription{instruments: ids}
	if p.has("--candles") {
		sub.candleInterval = firstNonEmpty(p.flag("--candles"), "1m")
	}
	if p.has("--orderbook") {
		sub.orderbookDepth = int32(parseInt64(firstNonEmpty(p.flag("--orderbook"), "20")))
	}
	sub.trades = p.has("--trades")
	sub.lastPrice = p.has("--last-price")
	sub.info = p.has("--info")
	if sub.candleInterval == "" && sub.orderbookDepth == 0 && !sub.trades && !sub.lastPrice && !sub.info {
		return marketDataSubscription{}, &errorBody{Code: "USAGE", Message: "select at least one of --candles, --orderbook, --trades, --last-price, or --info"}
	}

	if sub.candleInterval != "" {
		enumName, ok := candleIntervalNames[sub.candleInterval]
		if !ok {
			return marketDataSubscription{}, &errorBody{Code: "USAGE", Message: fmt.Sprintf("invalid candle interval %q: want 1m, 2m, 3m, 5m, 10m, 15m, 30m, 1h, 2h, 4h, 1d, 1w, or 1M", sub.candleInterval)}
		}
		sub.candleEnum = enumName
	}
	if sub.orderbookDepth != 0 && !validOrderbookDepths[sub.orderbookDepth] {
		return marketDataSubscription{}, &errorBody{Code: "USAGE", Message: fmt.Sprintf("invalid orderbook depth %d: want one of 1, 10, 20, 30, 40, 50", sub.orderbookDepth)}
	}

	for _, id := range ids {
		if !validInstrumentID(id) {
			return marketDataSubscription{}, &errorBody{Code: "USAGE", Message: fmt.Sprintf("unrecognized instrument identifier %q: want instrument_uid, FIGI, or TICKER@CLASSCODE", id)}
		}
	}
	return sub, nil
}

func dedupeStrings(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// runMarketDataStream validates and resolves a `stream marketdata` request,
// then plays the scenario's script filtered to what was actually subscribed.
// An unresolvable ticker@classcode/FIGI alias is BROKER_REJECTED (exit 5),
// mirroring classifyResolveErr's mapping of a broker NOT_FOUND in the sibling
// tinvest repo (cmd/tinvest/instruments.go, internal/render/errors.go) — the
// same classification the fake already uses for an unknown instrument on
// every unary command.
func runMarketDataStream(ctx context.Context, s *scenario, p parsedArgs, w io.Writer) int {
	sub, errBody := parseMarketDataSubscription(p)
	if errBody != nil {
		_ = writeFrame(w, s.errorFrame(errBody.Code, errBody.Message, s.AccountID))
		return exitForCode(errBody.Code)
	}
	uids := make(map[string]bool, len(sub.instruments))
	for _, id := range sub.instruments {
		uid, ok := s.resolveSubscriptionUID(id)
		if !ok {
			_ = writeFrame(w, s.errorFrame("BROKER_REJECTED", fmt.Sprintf("instrument %q not found", id), s.AccountID))
			return exitForCode("BROKER_REJECTED")
		}
		uids[uid] = true
	}
	return runStream(ctx, s, w, sub, uids)
}

// matchesSubscription reports whether a script entry would have been
// delivered under sub: lifecycle, control, and explicit error entries always
// pass (they carry no instrument_uid/kind to filter on); a
// candle/last_price/orderbook/trade/info entry passes only when its
// instrument_uid was subscribed and its kind (and, for a candle, its
// interval) was requested.
func matchesSubscription(e scriptEntry, sub marketDataSubscription, uids map[string]bool) bool {
	switch e.Type {
	case "candle", "last_price", "orderbook", "trade", "info":
	default:
		return true
	}
	var peek struct {
		InstrumentUID string `json:"instrument_uid"`
		Interval      string `json:"interval"`
	}
	_ = json.Unmarshal(e.Data, &peek)
	if peek.InstrumentUID == "" || !uids[peek.InstrumentUID] {
		return false
	}
	switch e.Type {
	case "candle":
		return sub.candleInterval != "" && peek.Interval == sub.candleEnum
	case "last_price":
		return sub.lastPrice
	case "orderbook":
		return sub.orderbookDepth != 0
	case "trade":
		return sub.trades
	case "info":
		return sub.info
	}
	return true
}

// scriptEntry is one line of a stream script. A normal frame sets Type (and
// usually Data); an entry that sets Exit terminates the process with that code
// after emitting its frame, simulating a mid-stream process death for the
// robot's stream supervisor to restart.
type scriptEntry struct {
	Type      string          `json:"type"`
	Time      string          `json:"time"`
	AccountID string          `json:"account_id"`
	Data      json.RawMessage `json:"data"`
	Error     *errorBody      `json:"error"`
	DelayMS   int64           `json:"delay_ms"`
	Exit      *int            `json:"exit"`
}

// runStream plays a scenario's `stream marketdata` script as NDJSON, filtered
// to the given subscription: a market-data entry is written only when
// matchesSubscription allows it. Pacing (delay_ms) and a scripted mid-stream
// exit are honored unconditionally regardless of filtering — a real broker
// would simply never generate an unsubscribed event rather than generate and
// then withhold it, but the script's delay/exit entries model the fake
// process's own timeline (used to hold the stream open for cancellation
// tests), which does not depend on what got filtered out of the log. It
// honors context cancellation (the robot's cancel -> SIGTERM shutdown) by
// emitting a final "disconnected" shutdown frame and exiting 0, matching the
// real CLI.
func runStream(ctx context.Context, s *scenario, w io.Writer, sub marketDataSubscription, uids map[string]bool) int {
	if s.Stream.Script == "" {
		_ = writeFrame(w, s.errorFrame("INTERNAL", "scenario configures no stream script", ""))
		return exitInternal
	}
	entries, err := loadScript(filepath.Join(s.dir, s.Stream.Script))
	if err != nil {
		_ = writeFrame(w, s.errorFrame("INTERNAL", err.Error(), ""))
		return exitInternal
	}

	for _, e := range entries {
		if ctx.Err() != nil {
			return s.emitShutdown(w)
		}
		if e.DelayMS > 0 {
			select {
			case <-ctx.Done():
				return s.emitShutdown(w)
			case <-time.After(time.Duration(e.DelayMS) * time.Millisecond):
			}
		}
		if (e.Type != "" || e.Error != nil) && (e.Type == "" || matchesSubscription(e, sub, uids)) {
			frame := streamFrame{
				Type:          e.Type,
				SchemaVersion: s.SchemaVersion,
				Time:          firstNonEmpty(e.Time, s.Stream.ShutdownTime, defaultStreamTime),
				AccountID:     e.AccountID,
				Data:          e.Data,
				Error:         e.Error,
			}
			if err := writeFrame(w, frame); err != nil {
				fmt.Fprintf(os.Stderr, "faketinvest: write stream frame: %v\n", err)
				return exitInternal
			}
		}
		if e.Exit != nil {
			return *e.Exit
		}
	}
	return exitOK
}

// emitShutdown writes the clean-shutdown lifecycle frame and returns exit 0.
func (s *scenario) emitShutdown(w io.Writer) int {
	frame := streamFrame{
		Type:          "disconnected",
		SchemaVersion: s.SchemaVersion,
		Time:          firstNonEmpty(s.Stream.ShutdownTime, defaultStreamTime),
		Data:          json.RawMessage(`{"reason":"shutdown","final":true}`),
	}
	if err := writeFrame(w, frame); err != nil {
		fmt.Fprintf(os.Stderr, "faketinvest: write shutdown frame: %v\n", err)
		return exitInternal
	}
	return exitOK
}

// errorFrame builds a stream error frame.
func (s *scenario) errorFrame(code, message, accountID string) streamFrame {
	return streamFrame{
		Type:          "error",
		SchemaVersion: s.SchemaVersion,
		Time:          firstNonEmpty(s.Stream.ShutdownTime, defaultStreamTime),
		AccountID:     accountID,
		Error:         &errorBody{Code: code, Message: message},
	}
}

// loadScript reads a stream script as either a JSON array of entries or a
// newline-delimited (NDJSON) list of entries.
func loadScript(path string) ([]scriptEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read stream script: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var entries []scriptEntry
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("decode stream script array: %w", err)
		}
		return entries, nil
	}
	var entries []scriptEntry
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for {
		var e scriptEntry
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode stream script line: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
