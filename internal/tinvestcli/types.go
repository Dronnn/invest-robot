package tinvestcli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Money is a decoded money or quotation value from the envelope. The wire form
// is an object {units, nano, value, currency?}; Amount is parsed from the exact
// decimal string in value (nine-digit fixed point, round-tripping model.Decimal)
// and Currency is empty for pure quotations (prices, ratios) that carry none.
type Money struct {
	Amount   model.Decimal
	Currency string
}

// UnmarshalJSON decodes the {units, nano, value, currency} object, taking the
// canonical decimal from value. A missing or malformed value is a decode error,
// surfaced by the calling method as a ProtocolError.
func (m *Money) UnmarshalJSON(b []byte) error {
	var w struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return fmt.Errorf("money object: %w", err)
	}
	d, err := model.ParseDecimal(w.Value)
	if err != nil {
		return fmt.Errorf("money value %q: %w", w.Value, err)
	}
	m.Amount = d
	m.Currency = w.Currency
	return nil
}

// int64String decodes an integer that the contract encodes as a JSON string
// (e.g. candle volume "1500", position balance "100"). A JSON number is also
// accepted for robustness.
type int64String int64

func (n *int64String) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*n = 0
		return nil
	}
	if s[0] == '"' {
		unq, err := strconv.Unquote(s)
		if err != nil {
			return fmt.Errorf("int string: %w", err)
		}
		s = strings.TrimSpace(unq)
	}
	if s == "" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("int string %q: %w", s, err)
	}
	*n = int64String(v)
	return nil
}

func (n int64String) Int64() int64 { return int64(n) }

// VersionInfo is the `version` data payload.
type VersionInfo struct {
	Version       string `json:"version"`
	Contract      string `json:"contract"`
	SchemaVersion string `json:"schema_version"`
	Go            string `json:"go"`
}

// Instrument is the full reference record from `instruments get`.
type Instrument struct {
	UID               string `json:"uid"`
	FIGI              string `json:"figi"`
	Ticker            string `json:"ticker"`
	ClassCode         string `json:"class_code"`
	Name              string `json:"name"`
	Type              string `json:"type"`
	Lot               int64  `json:"lot"`
	Currency          string `json:"currency"`
	MinPriceIncrement Money  `json:"min_price_increment"`
	TradingStatus     string `json:"trading_status"`
}

// Model maps the reference record onto the shared model.Instrument.
func (i Instrument) Model() model.Instrument {
	return model.Instrument{
		InstrumentRef: model.InstrumentRef{
			UID:       model.InstrumentUID(i.UID),
			FIGI:      model.FIGI(i.FIGI),
			Ticker:    i.Ticker,
			ClassCode: i.ClassCode,
		},
		Lot:               i.Lot,
		MinPriceIncrement: i.MinPriceIncrement.Amount,
		Currency:          i.Currency,
		Name:              i.Name,
	}
}

// InstrumentShort is one `instruments search` row.
type InstrumentShort struct {
	UID       string `json:"uid"`
	FIGI      string `json:"figi"`
	Ticker    string `json:"ticker"`
	ClassCode string `json:"class_code"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Lot       int64  `json:"lot"`
}

// LastPrice is one `quotes last` row.
type LastPrice struct {
	InstrumentUID string    `json:"instrument_uid"`
	Ticker        string    `json:"ticker"`
	ClassCode     string    `json:"class_code"`
	FIGI          string    `json:"figi"`
	Price         Money     `json:"price"`
	PriceType     string    `json:"price_type"`
	Time          time.Time `json:"time"`
}

// Quote maps the last-price row onto a model.Quote. Only the last price is
// known from this endpoint; Bid and Ask stay zero.
func (p LastPrice) Quote() model.Quote {
	return model.Quote{
		InstrumentUID: model.InstrumentUID(p.InstrumentUID),
		Last:          p.Price.Amount,
		TS:            p.Time.UTC(),
	}
}

// Candle is one bar from `candles get`.
type Candle struct {
	Time       time.Time   `json:"time"`
	Open       Money       `json:"open"`
	High       Money       `json:"high"`
	Low        Money       `json:"low"`
	Close      Money       `json:"close"`
	Volume     int64String `json:"volume"`
	VolumeBuy  int64String `json:"volume_buy"`
	VolumeSell int64String `json:"volume_sell"`
	IsComplete bool        `json:"is_complete"`
	Source     string      `json:"source"`
}

// CandlesResult is the `candles get` data: the request echo plus the bars.
type CandlesResult struct {
	InstrumentUID string    `json:"instrument_uid"`
	Interval      string    `json:"interval"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Candles       []Candle  `json:"candles"`
}

// Model maps the bars onto model.Candle values, stamping each with the result's
// instrument uid and interval (the wire carries those once, on the parent).
func (r CandlesResult) Model() ([]model.Candle, error) {
	interval, err := model.ParseCandleInterval(r.Interval)
	if err != nil {
		return nil, err
	}
	uid := model.InstrumentUID(r.InstrumentUID)
	out := make([]model.Candle, len(r.Candles))
	for i, c := range r.Candles {
		out[i] = model.Candle{
			InstrumentUID: uid,
			Interval:      interval,
			Open:          c.Open.Amount,
			High:          c.High.Amount,
			Low:           c.Low.Amount,
			Close:         c.Close.Amount,
			Volume:        c.Volume.Int64(),
			TS:            c.Time.UTC(),
			Complete:      c.IsComplete,
		}
	}
	return out, nil
}

// OrderbookLevel is one price level of a book side.
type OrderbookLevel struct {
	Price    Money       `json:"price"`
	Quantity int64String `json:"quantity"`
}

// Orderbook is the `orderbook get` data.
type Orderbook struct {
	InstrumentUID string           `json:"instrument_uid"`
	Ticker        string           `json:"ticker"`
	ClassCode     string           `json:"class_code"`
	FIGI          string           `json:"figi"`
	Depth         int              `json:"depth"`
	Bids          []OrderbookLevel `json:"bids"`
	Asks          []OrderbookLevel `json:"asks"`
	LastPrice     Money            `json:"last_price"`
	ClosePrice    Money            `json:"close_price"`
	LimitUp       Money            `json:"limit_up"`
	LimitDown     Money            `json:"limit_down"`
	Time          time.Time        `json:"orderbook_time"`
}

// Quote maps the book's top of book onto a model.Quote (best bid, best ask, and
// the reported last price).
func (o Orderbook) Quote() model.Quote {
	q := model.Quote{
		InstrumentUID: model.InstrumentUID(o.InstrumentUID),
		Last:          o.LastPrice.Amount,
		TS:            o.Time.UTC(),
	}
	if len(o.Bids) > 0 {
		q.Bid = o.Bids[0].Price.Amount
	}
	if len(o.Asks) > 0 {
		q.Ask = o.Asks[0].Price.Amount
	}
	return q
}

// Portfolio is the `portfolio get` data.
type Portfolio struct {
	AccountID             string              `json:"account_id"`
	TotalAmountPortfolio  Money               `json:"total_amount_portfolio"`
	TotalAmountShares     Money               `json:"total_amount_shares"`
	TotalAmountBonds      Money               `json:"total_amount_bonds"`
	TotalAmountEtf        Money               `json:"total_amount_etf"`
	TotalAmountCurrencies Money               `json:"total_amount_currencies"`
	TotalAmountFutures    Money               `json:"total_amount_futures"`
	TotalAmountOptions    Money               `json:"total_amount_options"`
	ExpectedYield         Money               `json:"expected_yield"`
	DailyYield            Money               `json:"daily_yield"`
	DailyYieldRelative    Money               `json:"daily_yield_relative"`
	Positions             []PortfolioPosition `json:"positions"`
	VirtualPositions      []json.RawMessage   `json:"virtual_positions"`
}

// PortfolioPosition is one holding within a portfolio.
type PortfolioPosition struct {
	InstrumentUID            string `json:"instrument_uid"`
	PositionUID              string `json:"position_uid"`
	FIGI                     string `json:"figi"`
	Ticker                   string `json:"ticker"`
	ClassCode                string `json:"class_code"`
	InstrumentType           string `json:"instrument_type"`
	Quantity                 Money  `json:"quantity"`
	AveragePositionPrice     Money  `json:"average_position_price"`
	AveragePositionPriceFIFO Money  `json:"average_position_price_fifo"`
	CurrentPrice             Money  `json:"current_price"`
	ExpectedYield            Money  `json:"expected_yield"`
	CurrentAccruedInterest   Money  `json:"current_accrued_interest"`
	Blocked                  bool   `json:"blocked"`
	BlockedLots              Money  `json:"blocked_lots"`
}

// Positions is the `positions get` data.
type Positions struct {
	AccountID               string             `json:"account_id"`
	Money                   []Money            `json:"money"`
	BlockedMoney            []Money            `json:"blocked_money"`
	Securities              []SecurityPosition `json:"securities"`
	Futures                 []json.RawMessage  `json:"futures"`
	Options                 []json.RawMessage  `json:"options"`
	LimitsLoadingInProgress bool               `json:"limits_loading_in_progress"`
}

// SecurityPosition is one security balance within a positions response.
type SecurityPosition struct {
	InstrumentUID   string      `json:"instrument_uid"`
	PositionUID     string      `json:"position_uid"`
	FIGI            string      `json:"figi"`
	Ticker          string      `json:"ticker"`
	ClassCode       string      `json:"class_code"`
	InstrumentType  string      `json:"instrument_type"`
	Balance         int64String `json:"balance"`
	Blocked         int64String `json:"blocked"`
	ExchangeBlocked bool        `json:"exchange_blocked"`
}

// Operation is one row of `operations list`.
type Operation struct {
	Cursor          string      `json:"cursor"`
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Date            time.Time   `json:"date"`
	Type            string      `json:"type"`
	State           string      `json:"state"`
	InstrumentUID   string      `json:"instrument_uid"`
	FIGI            string      `json:"figi"`
	Ticker          string      `json:"ticker"`
	ClassCode       string      `json:"class_code"`
	InstrumentType  string      `json:"instrument_type"`
	InstrumentKind  string      `json:"instrument_kind"`
	Payment         Money       `json:"payment"`
	Price           Money       `json:"price"`
	Commission      Money       `json:"commission"`
	Yield           Money       `json:"yield"`
	YieldRelative   Money       `json:"yield_relative"`
	AccruedInterest Money       `json:"accrued_interest"`
	Quantity        int64String `json:"quantity"`
	QuantityRest    int64String `json:"quantity_rest"`
	QuantityDone    int64String `json:"quantity_done"`
	TradeCount      int64       `json:"trade_count"`
}

// OperationsResult is the `operations list` data with its pagination cursor.
type OperationsResult struct {
	Operations []Operation `json:"operations"`
	NextCursor string      `json:"next_cursor"`
}

// Lots is the requested/executed/remaining lot triple on an order result.
type Lots struct {
	Requested int64 `json:"requested"`
	Executed  int64 `json:"executed"`
	Remaining int64 `json:"remaining"`
}

// Order is the placement result returned by `orders place`. It mirrors
// render.PlaceResultView: at placement only the initial commission is known, so
// Commission reads initial_commission (the order-state views report
// executed_commission instead — see OrderState). Money fields are pointers
// because the CLI omits them until they are known.
type Order struct {
	OrderID       string `json:"order_id"`
	ClientOrderID string `json:"client_order_id"`
	Lifecycle     string `json:"lifecycle"`
	Direction     string `json:"direction"`
	OrderType     string `json:"order_type"`
	Lots          Lots   `json:"lots"`
	InstrumentUID string `json:"instrument_uid"`
	Ticker        string `json:"ticker"`
	InitialPrice  *Money `json:"initial_order_price"`
	ExecutedPrice *Money `json:"executed_order_price"`
	TotalAmount   *Money `json:"total_order_amount"`
	Commission    *Money `json:"initial_commission"`
	Message       string `json:"message"`
}

// OrderState is one order's full state from `orders get` and each row of
// `orders list`. It mirrors render.OrderStateView, which differs from the
// placement view: it reports the realized executed_commission (not the initial
// estimate), and carries the order's currency and order_date. Money fields are
// pointers because the CLI omits them until they are known.
type OrderState struct {
	OrderID       string    `json:"order_id"`
	ClientOrderID string    `json:"client_order_id"`
	Lifecycle     string    `json:"lifecycle"`
	Direction     string    `json:"direction"`
	OrderType     string    `json:"order_type"`
	Lots          Lots      `json:"lots"`
	InstrumentUID string    `json:"instrument_uid"`
	Ticker        string    `json:"ticker"`
	Currency      string    `json:"currency"`
	InitialPrice  *Money    `json:"initial_order_price"`
	ExecutedPrice *Money    `json:"executed_order_price"`
	TotalAmount   *Money    `json:"total_order_amount"`
	Commission    *Money    `json:"executed_commission"`
	OrderDate     time.Time `json:"order_date"`
}

// CancelResult is the `orders cancel` data.
type CancelResult struct {
	OrderID string     `json:"order_id"`
	Time    *time.Time `json:"time"`
	Note    string     `json:"note"`
}

// ReconcileOutcome is one resolved (or still-unresolved) intent from
// `orders reconcile`. It mirrors render.ReconcileOutcomeView: Error carries a
// resolution failure and Note carries advisory context (e.g. that a match was
// heuristic), both of which the caller must surface rather than drop.
type ReconcileOutcome struct {
	IntentID      string `json:"intent_id"`
	ClientOrderID string `json:"client_order_id"`
	AccountID     string `json:"account_id"`
	Outcome       string `json:"outcome"`
	OrderID       string `json:"order_id"`
	Lifecycle     string `json:"lifecycle"`
	Error         string `json:"error"`
	Note          string `json:"note"`
}

// ReconcileResult is the `orders reconcile` data. UnresolvedCount > 0 is the
// success-with-unresolved case: the call exits 1 but ok stays true, and this
// count is the signal the caller acts on (DESIGN §4).
type ReconcileResult struct {
	Outcomes        []ReconcileOutcome `json:"outcomes"`
	UnresolvedCount int                `json:"unresolved_count"`
}

// StopOrder is one row of `stop-orders list`. It mirrors render.StopOrderView,
// whose lifecycle field is named status (a distinct enum from an order's
// lifecycle); reading it as lifecycle silently drops the stop order's state.
// The Phase-1 robot only lists stop orders, so extra renderer fields are left
// undecoded.
type StopOrder struct {
	StopOrderID   string `json:"stop_order_id"`
	Status        string `json:"status"`
	Direction     string `json:"direction"`
	StopOrderType string `json:"stop_order_type"`
	InstrumentUID string `json:"instrument_uid"`
	Ticker        string `json:"ticker"`
	Currency      string `json:"currency"`
	Price         *Money `json:"price"`
	StopPrice     *Money `json:"stop_price"`
}
