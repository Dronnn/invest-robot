package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ordersPlace synthesizes an `orders place` result from the request flags plus
// the scenario's placement defaults. The client order id is echoed from
// --order-id exactly as the real CLI does — the robot's idempotency and
// reconcile logic depends on that round-trip.
func (s *scenario) ordersPlace(p parsedArgs) unaryResult {
	clientOrderID := p.flag("--order-id")
	instrumentID := p.flag("--instrument")
	direction := p.flag("--direction")
	orderType := p.flag("--type")
	quantity := parseInt64(p.flag("--quantity"))

	var uid, ticker, currency string
	if instrumentID != "" {
		if !validInstrumentID(instrumentID) {
			return failResult("USAGE", fmt.Sprintf("unrecognized instrument identifier %q: want instrument_uid, FIGI, or TICKER@CLASSCODE", instrumentID))
		}
		inst, ok := s.resolveInstrument(instrumentID)
		if !ok {
			return failResult("BROKER_REJECTED", fmt.Sprintf("instrument %q not found", instrumentID))
		}
		uid, ticker, currency = inst.UID, inst.Ticker, inst.Currency
	}

	place := s.Orders.Place
	if currency == "" {
		currency = place.Currency
	}
	if currency == "" {
		currency = "rub"
	}

	executed := int64(0)
	if strings.Contains(place.Lifecycle, "FILL") && !strings.Contains(place.Lifecycle, "PARTIALLY") {
		executed = quantity
	}
	remaining := quantity - executed
	if remaining < 0 {
		remaining = 0
	}

	view := placeResultView{
		OrderID:       s.Orders.OrderIDPrefix + firstNonEmpty(clientOrderID, "1"),
		ClientOrderID: clientOrderID,
		Lifecycle:     place.Lifecycle,
		Direction:     enumOr(directionEnum, direction),
		OrderType:     enumOr(orderTypeEnum, orderType),
		Lots:          lotsView{Requested: quantity, Executed: executed, Remaining: remaining},
		InstrumentUID: uid,
		Ticker:        ticker,
		Message:       place.Message,
	}

	// initial_order_price defaults to the limit price when the scenario does not
	// pin one.
	initialPrice := place.InitialPrice
	if initialPrice == "" && strings.EqualFold(orderType, "limit") {
		initialPrice = p.flag("--price")
	}
	var err error
	if view.InitialPrice, err = optionalMoney(initialPrice, currency); err != nil {
		return failResult("INTERNAL", err.Error())
	}
	if view.ExecutedPrice, err = optionalMoney(place.ExecutedPrice, currency); err != nil {
		return failResult("INTERNAL", err.Error())
	}
	if view.TotalAmount, err = optionalMoney(place.TotalAmount, currency); err != nil {
		return failResult("INTERNAL", err.Error())
	}
	if view.Commission, err = optionalMoney(place.Commission, currency); err != nil {
		return failResult("INTERNAL", err.Error())
	}

	return marshalData(map[string]placeResultView{"order": view})
}

// cancelData mirrors the `orders cancel` envelope data.
type cancelData struct {
	OrderID string `json:"order_id"`
	Time    string `json:"time,omitempty"`
	Note    string `json:"note,omitempty"`
}

// ordersCancel acknowledges a cancel request idempotently.
func (s *scenario) ordersCancel(p parsedArgs) unaryResult {
	if len(p.args) == 0 {
		return failResult("USAGE", "orders cancel requires an order id")
	}
	return marshalData(cancelData{OrderID: p.args[0]})
}

// ordersReconcile replays the reconcile fixture and derives the exit code from
// unresolved_count: reconcile stays ok:true but exits 1 while any intent is
// still in doubt (AGENTS.md reconcile rule).
func (s *scenario) ordersReconcile() unaryResult {
	res := s.fixtureResponse("orders_reconcile", `{"outcomes":[]}`)
	if res.errBody != nil {
		return res
	}
	var peek struct {
		UnresolvedCount int `json:"unresolved_count"`
	}
	_ = json.Unmarshal(res.data, &peek)
	if peek.UnresolvedCount > 0 {
		res.exit = exitInternal
	}
	return res
}

// optionalMoney builds a *decimal for an optional money string, returning nil
// when the string is empty so the field is omitted.
func optionalMoney(s, currency string) (*decimal, error) {
	if s == "" {
		return nil, nil
	}
	d, err := moneyDecimal(s, currency)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func enumOr(m map[string]string, key string) string {
	if v, ok := m[strings.ToLower(key)]; ok {
		return v
	}
	return key
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
