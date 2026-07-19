package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// scenario is the parsed scenario.toml plus the directory it was loaded from.
// It is the single source of truth for how the fake responds.
type scenario struct {
	dir string

	AccountID        string `toml:"account_id"`
	TrackingID       string `toml:"tracking_id"`
	SchemaVersion    string `toml:"schema_version"`
	Contract         string `toml:"contract"`
	Version          string `toml:"version"`
	GoVersion        string `toml:"go_version"`
	DefaultLatencyMS int64  `toml:"default_latency_ms"`

	Instruments []instrumentSpec `toml:"instruments"`

	// Responses maps an account-level command to a fixture file whose contents
	// are emitted verbatim as the envelope data. Keys use underscores:
	// portfolio, positions, operations, orders_list, orders_reconcile,
	// stop_orders, instruments_search.
	Responses map[string]string `toml:"responses"`

	Orders ordersSpec `toml:"orders"`
	Stream streamSpec `toml:"stream"`
	Fail   []failSpec `toml:"fail"`

	// index resolves an identifier (uid, figi, or TICKER@CLASSCODE) to its spec.
	index map[string]*instrumentSpec
}

// instrumentSpec is one instrument in the scenario universe plus its market
// snapshot. Reference fields mirror render.InstrumentView; the market fields
// drive quotes/orderbook/candles for that instrument.
type instrumentSpec struct {
	UID               string `toml:"uid"`
	FIGI              string `toml:"figi"`
	Ticker            string `toml:"ticker"`
	ClassCode         string `toml:"class_code"`
	Name              string `toml:"name"`
	Type              string `toml:"type"`
	Lot               int32  `toml:"lot"`
	Currency          string `toml:"currency"`
	MinPriceIncrement string `toml:"min_price_increment"`
	TradingStatus     string `toml:"trading_status"`

	LastPrice     string `toml:"last_price"`
	LastPriceTime string `toml:"last_price_time"`
	PriceType     string `toml:"price_type"`
	Orderbook     string `toml:"orderbook"` // fixture -> OrderBookView data
	Candles       string `toml:"candles"`   // fixture -> []CandleView data
}

// ordersSpec configures order-placement synthesis.
type ordersSpec struct {
	OrderIDPrefix string           `toml:"order_id_prefix"`
	Place         placeOutcomeSpec `toml:"place"`
}

// placeOutcomeSpec is the default result an `orders place` produces on success.
type placeOutcomeSpec struct {
	Lifecycle     string `toml:"lifecycle"`
	ExecutedPrice string `toml:"executed_price"`
	InitialPrice  string `toml:"initial_price"`
	TotalAmount   string `toml:"total_amount"`
	Commission    string `toml:"commission"`
	Currency      string `toml:"currency"`
	Message       string `toml:"message"`
}

// streamSpec points at the ordered NDJSON script for `stream marketdata`.
type streamSpec struct {
	Script       string `toml:"script"`
	ShutdownTime string `toml:"shutdown_time"`
}

// failSpec is one failure-injection rule.
type failSpec struct {
	Command      string `toml:"command"`
	OnCall       int    `toml:"on_call"` // 1-based; 0 or omitted means every matching call
	Code         string `toml:"code"`
	Exit         *int   `toml:"exit"` // overrides the code-to-exit mapping when set
	Message      string `toml:"message"`
	Retryable    bool   `toml:"retryable"`
	RetryAfterMS int64  `toml:"retry_after_ms"`
	Phase        string `toml:"phase"`
	// ReconcileCommand and ReconcileOrderID populate error.reconcile for an
	// UNCONFIRMED (exit 7) failure; ReconcileOrderID defaults to the request's
	// --order-id when empty.
	ReconcileCommand string `toml:"reconcile_command"`
	ReconcileOrderID string `toml:"reconcile_order_id"`
}

// loadScenario reads scenario.toml (or scenario.json) from dir and builds the
// instrument index.
func loadScenario(dir string) (*scenario, error) {
	s := &scenario{dir: dir}
	tomlPath := filepath.Join(dir, "scenario.toml")
	jsonPath := filepath.Join(dir, "scenario.json")
	switch {
	case fileExists(tomlPath):
		if _, err := toml.DecodeFile(tomlPath, s); err != nil {
			return nil, fmt.Errorf("decode %s: %w", tomlPath, err)
		}
	case fileExists(jsonPath):
		raw, err := os.ReadFile(jsonPath)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, s); err != nil {
			return nil, fmt.Errorf("decode %s: %w", jsonPath, err)
		}
	default:
		return nil, fmt.Errorf("no scenario.toml or scenario.json in %s", dir)
	}

	if s.SchemaVersion == "" {
		s.SchemaVersion = defaultSchemaVersion
	}
	if s.Contract == "" {
		s.Contract = defaultContract
	}
	if s.Version == "" {
		s.Version = defaultVersion
	}
	if s.GoVersion == "" {
		s.GoVersion = defaultGoVersion
	}
	if s.Orders.OrderIDPrefix == "" {
		s.Orders.OrderIDPrefix = "ord-"
	}
	if s.Orders.Place.Lifecycle == "" {
		s.Orders.Place.Lifecycle = "EXECUTION_REPORT_STATUS_FILL"
	}

	s.index = map[string]*instrumentSpec{}
	for i := range s.Instruments {
		inst := &s.Instruments[i]
		if inst.UID != "" {
			s.index[inst.UID] = inst
		}
		if inst.FIGI != "" {
			s.index[inst.FIGI] = inst
		}
		if inst.Ticker != "" && inst.ClassCode != "" {
			s.index[inst.Ticker+"@"+inst.ClassCode] = inst
		}
	}
	return s, nil
}

// resolveInstrument maps an identifier to its spec, mirroring the real
// resolver's accepted id shapes (uid, FIGI, or TICKER@CLASSCODE).
func (s *scenario) resolveInstrument(id string) (*instrumentSpec, bool) {
	inst, ok := s.index[id]
	return inst, ok
}

// readFixture loads a fixture file relative to the scenario directory and
// returns its bytes as raw JSON, validating that it parses.
func (s *scenario) readFixture(rel string) (json.RawMessage, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, rel))
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("fixture %s is not valid JSON", rel)
	}
	return json.RawMessage(raw), nil
}

// validInstrumentID reports whether an identifier is syntactically a uid, FIGI,
// or TICKER@CLASSCODE — the check the real CLI applies before any network call
// (a malformed id is a usage error, exit 2).
func validInstrumentID(id string) bool {
	if id == "" {
		return false
	}
	if strings.Contains(id, "@") {
		parts := strings.SplitN(id, "@", 2)
		return parts[0] != "" && parts[1] != "" && isIDChars(id)
	}
	return isIDChars(id)
}

func isIDChars(id string) bool {
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '@':
		default:
			return false
		}
	}
	return true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
