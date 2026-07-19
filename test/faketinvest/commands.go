package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// unaryResult is the outcome of building one unary command's response. Exactly
// one of data/errBody is meaningful: errBody != nil means an ok:false envelope.
// exit is the process exit code — normally 0 on success, but `orders reconcile`
// exits 1 while still reporting ok:true when intents remain unresolved.
type unaryResult struct {
	data    json.RawMessage
	errBody *errorBody
	exit    int
}

func okResult(data json.RawMessage) unaryResult { return unaryResult{data: data, exit: exitOK} }

func failResult(code, message string) unaryResult {
	return unaryResult{errBody: &errorBody{Code: code, Message: message}, exit: exitForCode(code)}
}

// buildUnary produces the response for a non-stream, non-version command.
func buildUnary(s *scenario, p parsedArgs) unaryResult {
	switch p.command {
	case "instruments get":
		return s.instrumentGet(p)
	case "instruments search":
		return s.instrumentsSearch(p)
	case "quotes last":
		return s.quotesLast(p)
	case "candles get":
		return s.candlesGet(p)
	case "orderbook get":
		return s.orderbookGet(p)
	case "portfolio get":
		return s.fixtureResponse("portfolio", "")
	case "positions get":
		return s.fixtureResponse("positions", "")
	case "operations list":
		return s.fixtureResponse("operations", `{"operations":[],"next_cursor":""}`)
	case "orders list":
		return s.fixtureResponse("orders_list", `{"orders":[]}`)
	case "orders get":
		return s.fixtureResponse("orders_get", "")
	case "orders cancel":
		return s.ordersCancel(p)
	case "orders place":
		return s.ordersPlace(p)
	case "orders reconcile":
		return s.ordersReconcile()
	case "stop-orders list":
		return s.fixtureResponse("stop_orders", `{"stop_orders":[]}`)
	default:
		return failResult("USAGE", fmt.Sprintf("unknown command %q", p.command))
	}
}

// fixtureResponse emits an account-level fixture verbatim as the data payload.
// When no fixture is configured it falls back to defaultJSON (an error if that
// is empty).
func (s *scenario) fixtureResponse(key, defaultJSON string) unaryResult {
	rel := s.Responses[key]
	if rel == "" {
		if defaultJSON == "" {
			return failResult("INTERNAL", fmt.Sprintf("scenario has no [responses] fixture for %q", key))
		}
		return okResult(json.RawMessage(defaultJSON))
	}
	data, err := s.readFixture(rel)
	if err != nil {
		return failResult("INTERNAL", fmt.Sprintf("read fixture %q: %v", rel, err))
	}
	return okResult(data)
}

// instrumentGet resolves one identifier to its full reference record.
func (s *scenario) instrumentGet(p parsedArgs) unaryResult {
	if len(p.args) == 0 {
		return failResult("USAGE", "instruments get requires an instrument identifier")
	}
	id := p.args[0]
	if !validInstrumentID(id) {
		return failResult("USAGE", fmt.Sprintf("unrecognized instrument identifier %q: want instrument_uid, FIGI, or TICKER@CLASSCODE", id))
	}
	inst, ok := s.resolveInstrument(id)
	if !ok {
		return failResult("BROKER_REJECTED", fmt.Sprintf("instrument %q not found", id))
	}
	view, err := s.instrumentViewOf(inst)
	if err != nil {
		return failResult("INTERNAL", err.Error())
	}
	return marshalData(map[string]instrumentView{"instrument": view})
}

// instrumentsSearch returns matching instruments. A configured fixture wins;
// otherwise the universe is filtered by a case-insensitive substring on ticker
// or name.
func (s *scenario) instrumentsSearch(p parsedArgs) unaryResult {
	if rel := s.Responses["instruments_search"]; rel != "" {
		data, err := s.readFixture(rel)
		if err != nil {
			return failResult("INTERNAL", fmt.Sprintf("read fixture %q: %v", rel, err))
		}
		return okResult(data)
	}
	query := ""
	if len(p.args) > 0 {
		query = strings.ToLower(p.args[0])
	}
	hits := []instrumentShortView{}
	for i := range s.Instruments {
		inst := &s.Instruments[i]
		if query == "" || strings.Contains(strings.ToLower(inst.Ticker), query) ||
			strings.Contains(strings.ToLower(inst.Name), query) {
			hits = append(hits, instrumentShortView{
				UID: inst.UID, FIGI: inst.FIGI, Ticker: inst.Ticker, ClassCode: inst.ClassCode,
				Name: inst.Name, Type: inst.Type, Lot: inst.Lot,
			})
		}
	}
	return marshalData(map[string][]instrumentShortView{"instruments": hits})
}

// quotesLast resolves every requested id and emits one last-price row each.
func (s *scenario) quotesLast(p parsedArgs) unaryResult {
	if len(p.args) == 0 {
		return failResult("USAGE", "quotes last requires at least one instrument identifier")
	}
	rows := make([]lastPriceView, 0, len(p.args))
	for _, id := range p.args {
		if !validInstrumentID(id) {
			return failResult("USAGE", fmt.Sprintf("unrecognized instrument identifier %q: want instrument_uid, FIGI, or TICKER@CLASSCODE", id))
		}
		inst, ok := s.resolveInstrument(id)
		if !ok {
			return failResult("BROKER_REJECTED", fmt.Sprintf("instrument %q not found", id))
		}
		price, err := quotationDecimal(inst.LastPrice)
		if err != nil {
			return failResult("INTERNAL", fmt.Sprintf("instrument %s last_price: %v", inst.Ticker, err))
		}
		priceType := inst.PriceType
		if priceType == "" {
			priceType = "LAST_PRICE_EXCHANGE"
		}
		rows = append(rows, lastPriceView{
			InstrumentUID: inst.UID, Ticker: inst.Ticker, ClassCode: inst.ClassCode, FIGI: inst.FIGI,
			Price: price, PriceType: priceType, Time: inst.LastPriceTime,
		})
	}
	return marshalData(map[string][]lastPriceView{"last_prices": rows})
}

// candlesGetData mirrors the `candles get` envelope data: the request echo plus
// the candle list, whose element order is preserved from the fixture.
type candlesGetData struct {
	InstrumentUID string          `json:"instrument_uid"`
	Interval      string          `json:"interval"`
	From          string          `json:"from"`
	To            string          `json:"to"`
	Candles       json.RawMessage `json:"candles"`
}

// candlesGet echoes the request and replays the instrument's candle fixture.
func (s *scenario) candlesGet(p parsedArgs) unaryResult {
	if len(p.args) == 0 {
		return failResult("USAGE", "candles get requires an instrument identifier")
	}
	id := p.args[0]
	if !validInstrumentID(id) {
		return failResult("USAGE", fmt.Sprintf("unrecognized instrument identifier %q: want instrument_uid, FIGI, or TICKER@CLASSCODE", id))
	}
	// The real CLI parses a required --from/--to range before resolving the
	// instrument (parseRequiredTimeRange), so a missing or malformed bound is a
	// usage error that never reaches the network.
	from, to, errBody := parseCandleRange(p.flag("--from"), p.flag("--to"))
	if errBody != nil {
		return unaryResult{errBody: errBody, exit: exitUsage}
	}
	inst, ok := s.resolveInstrument(id)
	if !ok {
		return failResult("BROKER_REJECTED", fmt.Sprintf("instrument %q not found", id))
	}
	// After resolution the real CLI's CandleWindows requires from < to and
	// rejects from >= to as a usage error. A rollover confirm that fetches a
	// single instant (from == to) must fail here, never silently succeed.
	if !from.Before(to) {
		return failResult("USAGE", "candle --from must be before --to")
	}
	candles := json.RawMessage("[]")
	if inst.Candles != "" {
		raw, err := s.readFixture(inst.Candles)
		if err != nil {
			return failResult("INTERNAL", fmt.Sprintf("read candles fixture %q: %v", inst.Candles, err))
		}
		candles = raw
	}
	data := candlesGetData{
		InstrumentUID: inst.UID,
		Interval:      p.flag("--interval"),
		From:          p.flag("--from"),
		To:            p.flag("--to"),
		Candles:       candles,
	}
	return marshalData(data)
}

// parseCandleRange mirrors the real CLI's parseRequiredTimeRange: both bounds
// are required and must parse as RFC3339. Ordering (from < to) is checked
// separately, after instrument resolution, to match CandleWindows.
func parseCandleRange(fromRaw, toRaw string) (time.Time, time.Time, *errorBody) {
	if fromRaw == "" {
		return time.Time{}, time.Time{}, &errorBody{Code: "USAGE", Message: "--from is required"}
	}
	if toRaw == "" {
		return time.Time{}, time.Time{}, &errorBody{Code: "USAGE", Message: "--to is required"}
	}
	from, err := time.Parse(time.RFC3339, fromRaw)
	if err != nil {
		return time.Time{}, time.Time{}, &errorBody{Code: "USAGE", Message: fmt.Sprintf("invalid --from %q: %v", fromRaw, err)}
	}
	to, err := time.Parse(time.RFC3339, toRaw)
	if err != nil {
		return time.Time{}, time.Time{}, &errorBody{Code: "USAGE", Message: fmt.Sprintf("invalid --to %q: %v", toRaw, err)}
	}
	return from, to, nil
}

// orderbookGet replays the instrument's order-book fixture under the "orderbook"
// key.
func (s *scenario) orderbookGet(p parsedArgs) unaryResult {
	if len(p.args) == 0 {
		return failResult("USAGE", "orderbook get requires an instrument identifier")
	}
	id := p.args[0]
	if !validInstrumentID(id) {
		return failResult("USAGE", fmt.Sprintf("unrecognized instrument identifier %q: want instrument_uid, FIGI, or TICKER@CLASSCODE", id))
	}
	inst, ok := s.resolveInstrument(id)
	if !ok {
		return failResult("BROKER_REJECTED", fmt.Sprintf("instrument %q not found", id))
	}
	if inst.Orderbook == "" {
		return failResult("INTERNAL", fmt.Sprintf("instrument %s has no orderbook fixture", inst.Ticker))
	}
	raw, err := s.readFixture(inst.Orderbook)
	if err != nil {
		return failResult("INTERNAL", fmt.Sprintf("read orderbook fixture %q: %v", inst.Orderbook, err))
	}
	return marshalData(map[string]json.RawMessage{"orderbook": raw})
}

// instrumentViewOf builds the full reference view for an instrument spec.
func (s *scenario) instrumentViewOf(inst *instrumentSpec) (instrumentView, error) {
	inc, err := quotationDecimal(inst.MinPriceIncrement)
	if err != nil {
		return instrumentView{}, fmt.Errorf("instrument %s min_price_increment: %w", inst.Ticker, err)
	}
	return instrumentView{
		UID: inst.UID, FIGI: inst.FIGI, Ticker: inst.Ticker, ClassCode: inst.ClassCode,
		Name: inst.Name, Type: inst.Type, Lot: inst.Lot, Currency: inst.Currency,
		MinPriceIncrement: inc, TradingStatus: inst.TradingStatus,
	}, nil
}

// marshalData marshals a value into an envelope data payload.
func marshalData(v any) unaryResult {
	raw, err := json.Marshal(v)
	if err != nil {
		return failResult("INTERNAL", fmt.Sprintf("marshal response: %v", err))
	}
	return okResult(raw)
}
