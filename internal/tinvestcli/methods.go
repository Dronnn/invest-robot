package tinvestcli

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// PlaceRequest is the input to OrdersPlace. LimitPrice is required for a limit
// order and ignored for a market order.
type PlaceRequest struct {
	Account      string
	InstrumentID string
	Direction    model.Side
	Quantity     int64
	Type         model.OrderType
	LimitPrice   *model.Decimal
	TimeInForce  model.TimeInForce
	// OrderID is the stable client order id (idempotency key), passed as
	// --order-id. The tinvest CLI validates it as a UUID and rejects anything
	// else with a usage error, so it MUST be a UUID; the order-intent journal (a
	// later step) generates it. Left empty, the CLI would mint its own, breaking
	// the robot's idempotency — callers on the trading path always set it.
	OrderID string
}

// Version returns the CLI's version payload. Prefer Handshake at startup; this
// is for later re-checks.
func (c *Client) Version(ctx context.Context) (VersionInfo, error) {
	raw, _, err := c.call(ctx, callSpec{grp: groupUsers, argv: []string{"version"}, timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return VersionInfo{}, err
	}
	var v VersionInfo
	if err := decodeData(raw, &v); err != nil {
		return VersionInfo{}, err
	}
	return v, nil
}

// InstrumentGet resolves one instrument by uid, FIGI, or TICKER@CLASSCODE.
func (c *Client) InstrumentGet(ctx context.Context, id string) (Instrument, error) {
	raw, _, err := c.call(ctx, callSpec{grp: groupInstruments, argv: []string{"instruments", "get", id}, timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return Instrument{}, err
	}
	var w struct {
		Instrument Instrument `json:"instrument"`
	}
	if err := decodeData(raw, &w); err != nil {
		return Instrument{}, err
	}
	return w.Instrument, nil
}

// InstrumentsSearch returns instruments matching a free-text query.
func (c *Client) InstrumentsSearch(ctx context.Context, query string) ([]InstrumentShort, error) {
	raw, _, err := c.call(ctx, callSpec{grp: groupInstruments, argv: []string{"instruments", "search", query}, timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return nil, err
	}
	var w struct {
		Instruments []InstrumentShort `json:"instruments"`
	}
	if err := decodeData(raw, &w); err != nil {
		return nil, err
	}
	return w.Instruments, nil
}

// QuotesLast returns the last price for each instrument in one batched call (the
// CLI accepts a variadic id list, which is how the robot avoids one process per
// instrument).
func (c *Client) QuotesLast(ctx context.Context, ids []string) ([]LastPrice, error) {
	argv := append([]string{"quotes", "last"}, ids...)
	raw, _, err := c.call(ctx, callSpec{grp: groupMarketData, argv: argv, timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return nil, err
	}
	var w struct {
		LastPrices []LastPrice `json:"last_prices"`
	}
	if err := decodeData(raw, &w); err != nil {
		return nil, err
	}
	return w.LastPrices, nil
}

// CandlesGet downloads candles for an instrument over [from, to] at the given
// interval. It uses the longer candles deadline.
func (c *Client) CandlesGet(ctx context.Context, id string, interval model.CandleInterval, from, to time.Time) (CandlesResult, error) {
	argv := []string{
		"candles", "get", id,
		"--interval", interval.String(),
		"--from", from.UTC().Format(time.RFC3339),
		"--to", to.UTC().Format(time.RFC3339),
	}
	raw, _, err := c.call(ctx, callSpec{grp: groupMarketData, argv: argv, timeout: c.cfg.CandlesTimeout, read: true})
	if err != nil {
		return CandlesResult{}, err
	}
	var res CandlesResult
	if err := decodeData(raw, &res); err != nil {
		return CandlesResult{}, err
	}
	return res, nil
}

// OrderbookGet returns the order book for an instrument at the requested depth.
func (c *Client) OrderbookGet(ctx context.Context, id string, depth int) (Orderbook, error) {
	argv := []string{"orderbook", "get", id}
	if depth > 0 {
		argv = append(argv, "--depth", strconv.Itoa(depth))
	}
	raw, _, err := c.call(ctx, callSpec{grp: groupMarketData, argv: argv, timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return Orderbook{}, err
	}
	var w struct {
		Orderbook Orderbook `json:"orderbook"`
	}
	if err := decodeData(raw, &w); err != nil {
		return Orderbook{}, err
	}
	return w.Orderbook, nil
}

// PortfolioGet returns the account portfolio.
func (c *Client) PortfolioGet(ctx context.Context, account string) (Portfolio, error) {
	raw, _, err := c.call(ctx, callSpec{grp: groupOperations, argv: accountArgv("portfolio", "get", account), timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return Portfolio{}, err
	}
	var w struct {
		Portfolio Portfolio `json:"portfolio"`
	}
	if err := decodeData(raw, &w); err != nil {
		return Portfolio{}, err
	}
	return w.Portfolio, nil
}

// PositionsGet returns the account's money and security balances.
func (c *Client) PositionsGet(ctx context.Context, account string) (Positions, error) {
	raw, _, err := c.call(ctx, callSpec{grp: groupOperations, argv: accountArgv("positions", "get", account), timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return Positions{}, err
	}
	var w struct {
		Positions Positions `json:"positions"`
	}
	if err := decodeData(raw, &w); err != nil {
		return Positions{}, err
	}
	return w.Positions, nil
}

// OperationsList returns a page of account operations. cursor and limit are
// optional (empty/zero to omit).
func (c *Client) OperationsList(ctx context.Context, account, cursor string, limit int) (OperationsResult, error) {
	argv := accountArgv("operations", "list", account)
	if cursor != "" {
		argv = append(argv, "--cursor", cursor)
	}
	if limit > 0 {
		argv = append(argv, "--limit", strconv.Itoa(limit))
	}
	raw, _, err := c.call(ctx, callSpec{grp: groupOperations, argv: argv, timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return OperationsResult{}, err
	}
	var res OperationsResult
	if err := decodeData(raw, &res); err != nil {
		return OperationsResult{}, err
	}
	return res, nil
}

// OrdersPlace submits an order. It is a mutation: it never retries, so a
// NetworkError, RateLimitError, or OutcomeUnknownError surfaces to the caller
// for journaling/reconciliation. req.OrderID must be a UUID (the CLI rejects a
// non-UUID client order id with a usage error).
func (c *Client) OrdersPlace(ctx context.Context, req PlaceRequest) (Order, error) {
	// Fail before spawning: an empty or malformed order id would let the CLI
	// mint one the journal never sees, breaking idempotency and reconciliation.
	if !validClientOrderID(req.OrderID) {
		return Order{}, &UsageError{BrokerError: BrokerError{
			Code:    "USAGE",
			Message: "orders place requires a UUID order id (idempotency key)",
		}}
	}
	argv := accountArgv("orders", "place", req.Account)
	if req.InstrumentID != "" {
		argv = append(argv, "--instrument", req.InstrumentID)
	}
	if req.Direction != "" {
		argv = append(argv, "--direction", req.Direction.String())
	}
	if req.Quantity != 0 {
		argv = append(argv, "--quantity", strconv.FormatInt(req.Quantity, 10))
	}
	if req.Type != "" {
		argv = append(argv, "--type", req.Type.String())
	}
	if req.LimitPrice != nil {
		argv = append(argv, "--price", req.LimitPrice.String())
	}
	if req.TimeInForce != "" {
		argv = append(argv, "--tif", req.TimeInForce.String())
	}
	argv = append(argv, "--order-id", req.OrderID)
	raw, _, err := c.call(ctx, callSpec{grp: groupOrders, argv: argv, timeout: c.cfg.Timeout, read: false, orderID: req.OrderID})
	if err != nil {
		return Order{}, err
	}
	return decodeOrder(raw)
}

// OrdersGet fetches one order's full state by its exchange order id.
func (c *Client) OrdersGet(ctx context.Context, account, orderID string) (OrderState, error) {
	argv := append(accountArgv("orders", "get", account), orderID)
	raw, _, err := c.call(ctx, callSpec{grp: groupOrders, argv: argv, timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return OrderState{}, err
	}
	var w struct {
		Order OrderState `json:"order"`
	}
	if err := decodeData(raw, &w); err != nil {
		return OrderState{}, err
	}
	return w.Order, nil
}

// OrdersCancel cancels one order by id. It is a mutation and never retries.
func (c *Client) OrdersCancel(ctx context.Context, account, orderID string) (CancelResult, error) {
	argv := append(accountArgv("orders", "cancel", account), orderID)
	raw, _, err := c.call(ctx, callSpec{grp: groupOrders, argv: argv, timeout: c.cfg.Timeout, read: false, orderID: orderID})
	if err != nil {
		return CancelResult{}, err
	}
	var res CancelResult
	if err := decodeData(raw, &res); err != nil {
		return CancelResult{}, err
	}
	return res, nil
}

// OrdersList returns the account's active orders as full states.
func (c *Client) OrdersList(ctx context.Context, account string) ([]OrderState, error) {
	raw, _, err := c.call(ctx, callSpec{grp: groupOrders, argv: accountArgv("orders", "list", account), timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return nil, err
	}
	var w struct {
		Orders []OrderState `json:"orders"`
	}
	if err := decodeData(raw, &w); err != nil {
		return nil, err
	}
	return w.Orders, nil
}

// OrdersReconcile resolves the state of intents whose outcome was unknown. It is
// treated as a mutation-adjacent recovery call (no auto-retry) and honors the
// documented ok:true / exit 1 result: a non-zero UnresolvedCount is returned as
// success, not an error, for the caller to act on.
func (c *Client) OrdersReconcile(ctx context.Context, account string) (ReconcileResult, error) {
	raw, _, err := c.call(ctx, callSpec{
		grp:                 groupOrders,
		argv:                accountArgv("orders", "reconcile", account),
		timeout:             c.cfg.Timeout,
		read:                false,
		allowReconcileExit1: true,
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	var res ReconcileResult
	if err := decodeData(raw, &res); err != nil {
		return ReconcileResult{}, err
	}
	return res, nil
}

// StopOrdersList returns the account's stop orders.
func (c *Client) StopOrdersList(ctx context.Context, account string) ([]StopOrder, error) {
	raw, _, err := c.call(ctx, callSpec{grp: groupOrders, argv: accountArgv("stop-orders", "list", account), timeout: c.cfg.Timeout, read: true})
	if err != nil {
		return nil, err
	}
	var w struct {
		StopOrders []StopOrder `json:"stop_orders"`
	}
	if err := decodeData(raw, &w); err != nil {
		return nil, err
	}
	return w.StopOrders, nil
}

// validClientOrderID reports whether s is a canonical RFC 4122 UUID string
// (8-4-4-4-12, hyphenated hex). The tinvest CLI rejects any other --order-id
// with a usage error, so the adapter checks the shape before spawning rather
// than paying for a subprocess to learn the id was malformed.
func validClientOrderID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// accountArgv builds a "<group> <sub>" command with an optional --account flag.
func accountArgv(group, sub, account string) []string {
	argv := []string{group, sub}
	if account != "" {
		argv = append(argv, "--account", account)
	}
	return argv
}

// decodeOrder unwraps the {order} envelope key shared by place and get.
func decodeOrder(raw json.RawMessage) (Order, error) {
	var w struct {
		Order Order `json:"order"`
	}
	if err := decodeData(raw, &w); err != nil {
		return Order{}, err
	}
	return w.Order, nil
}

// decodeData unmarshals an envelope data payload, reporting a malformed shape as
// a ProtocolError (the CLI claimed success but sent an unusable body).
func decodeData(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return &ProtocolError{Reason: "malformed data payload", Err: err}
	}
	return nil
}
