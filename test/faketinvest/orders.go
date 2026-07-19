package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ordersPlace synthesizes an `orders place` result from the request flags plus
// the scenario's placement defaults. The client order id is echoed from
// --order-id exactly as the real CLI does — the robot's idempotency and
// reconcile logic depends on that round-trip.
//
// Validation order mirrors the real CLI (cmd/tinvest/orders.go runPlace): every
// local, no-network check — instrument syntax, direction/type/tif enums,
// order-id UUID shape, quantity, and price presence/format — runs before the
// instrument is resolved against the broker (here, the scenario universe), so
// a bad request fails USAGE/exit 2 without ever reaching BROKER_REJECTED.
func (s *scenario) ordersPlace(p parsedArgs) unaryResult {
	clientOrderID := p.flag("--order-id")
	if clientOrderID != "" {
		if err := validateOrderID(clientOrderID); err != nil {
			return failResult("USAGE", err.Error())
		}
	}

	instrumentID := p.flag("--instrument")
	if !validInstrumentID(instrumentID) {
		return failResult("USAGE", fmt.Sprintf("unrecognized instrument identifier %q: want instrument_uid, FIGI, or TICKER@CLASSCODE", instrumentID))
	}

	direction := p.flag("--direction")
	if direction != "buy" && direction != "sell" {
		return failResult("USAGE", fmt.Sprintf("invalid direction %q: want buy or sell", direction))
	}

	orderType := p.flag("--type")
	switch orderType {
	case "limit", "market", "bestprice":
	default:
		return failResult("USAGE", fmt.Sprintf("invalid order type %q: want limit, market, or bestprice", orderType))
	}

	if tif := p.flag("--tif"); tif != "" {
		switch tif {
		case "day", "ioc", "fok":
		default:
			return failResult("USAGE", fmt.Sprintf("invalid time-in-force %q: want day, ioc, or fok", tif))
		}
	}

	quantity := parseInt64(p.flag("--quantity"))
	if quantity <= 0 {
		return failResult("USAGE", fmt.Sprintf("quantity must be a positive number of lots, got %d", quantity))
	}

	priceFlag := p.flag("--price")
	if orderType == "limit" {
		if priceFlag == "" {
			return failResult("USAGE", "limit orders require a --price")
		}
	} else if priceFlag != "" {
		return failResult("USAGE", fmt.Sprintf("--price is not allowed for %s orders", orderType))
	}
	if priceFlag != "" {
		if _, err := quotationDecimal(priceFlag); err != nil {
			return failResult("USAGE", fmt.Sprintf("invalid --price %q: %v", priceFlag, err))
		}
	}

	inst, ok := s.resolveInstrument(instrumentID)
	if !ok {
		return failResult("BROKER_REJECTED", fmt.Sprintf("instrument %q not found", instrumentID))
	}
	uid, ticker, currency := inst.UID, inst.Ticker, inst.Currency

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

// validateOrderID checks the canonical 8-4-4-4-12 UUID shape, exactly as the
// real CLI's cmd/tinvest/orders.go validateOrderID does: 36 characters, dashes
// at positions 8/13/18/23, and valid hex everywhere else. It does not require
// the RFC 4122 version/variant bits — same as the real check, which rejects on
// shape only, not on which UUID version produced it.
func validateOrderID(orderID string) error {
	if len(orderID) != 36 || orderID[8] != '-' || orderID[13] != '-' || orderID[18] != '-' || orderID[23] != '-' {
		return fmt.Errorf("order-id must be a UUID in canonical 8-4-4-4-12 format")
	}
	hexText := strings.ReplaceAll(orderID, "-", "")
	if len(hexText) != 32 {
		return fmt.Errorf("order-id must be a UUID in canonical 8-4-4-4-12 format")
	}
	if _, err := hex.DecodeString(hexText); err != nil {
		return fmt.Errorf("order-id must be a UUID in canonical 8-4-4-4-12 format")
	}
	return nil
}
