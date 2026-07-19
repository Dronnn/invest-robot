package main

// The view structs below mirror the JSON shapes the real render package emits
// for the commands the fake synthesizes dynamically (rather than replaying a
// fixture verbatim). Field names, order, and types match render exactly.

// instrumentView mirrors render.InstrumentView (`instruments get`).
type instrumentView struct {
	UID               string  `json:"uid"`
	FIGI              string  `json:"figi"`
	Ticker            string  `json:"ticker"`
	ClassCode         string  `json:"class_code"`
	Name              string  `json:"name"`
	Type              string  `json:"type"`
	Lot               int32   `json:"lot"`
	Currency          string  `json:"currency"`
	MinPriceIncrement decimal `json:"min_price_increment"`
	TradingStatus     string  `json:"trading_status"`
}

// instrumentShortView mirrors render.InstrumentShortView (`instruments search`).
type instrumentShortView struct {
	UID       string `json:"uid"`
	FIGI      string `json:"figi"`
	Ticker    string `json:"ticker"`
	ClassCode string `json:"class_code"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Lot       int32  `json:"lot"`
}

// lastPriceView mirrors render.LastPriceView (one `quotes last` row).
type lastPriceView struct {
	InstrumentUID string  `json:"instrument_uid"`
	Ticker        string  `json:"ticker"`
	ClassCode     string  `json:"class_code"`
	FIGI          string  `json:"figi"`
	Price         decimal `json:"price"`
	PriceType     string  `json:"price_type"`
	Time          string  `json:"time"`
}

// lotsView mirrors render.LotsView.
type lotsView struct {
	Requested int64 `json:"requested"`
	Executed  int64 `json:"executed"`
	Remaining int64 `json:"remaining"`
}

// placeResultView mirrors render.PlaceResultView (`orders place`).
type placeResultView struct {
	OrderID       string   `json:"order_id"`
	ClientOrderID string   `json:"client_order_id"`
	Lifecycle     string   `json:"lifecycle"`
	Direction     string   `json:"direction"`
	OrderType     string   `json:"order_type"`
	Lots          lotsView `json:"lots"`
	InstrumentUID string   `json:"instrument_uid,omitempty"`
	Ticker        string   `json:"ticker,omitempty"`
	InitialPrice  *decimal `json:"initial_order_price,omitempty"`
	ExecutedPrice *decimal `json:"executed_order_price,omitempty"`
	TotalAmount   *decimal `json:"total_order_amount,omitempty"`
	Commission    *decimal `json:"initial_commission,omitempty"`
	Message       string   `json:"message,omitempty"`
}

// enum-name maps for the flag values the robot passes to `orders place`,
// matching the proto enum names the real CLI echoes back.
var directionEnum = map[string]string{
	"buy":  "ORDER_DIRECTION_BUY",
	"sell": "ORDER_DIRECTION_SELL",
}

var orderTypeEnum = map[string]string{
	"limit":     "ORDER_TYPE_LIMIT",
	"market":    "ORDER_TYPE_MARKET",
	"bestprice": "ORDER_TYPE_BESTPRICE",
}
