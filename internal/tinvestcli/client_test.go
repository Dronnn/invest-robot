package tinvestcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
)

// fakeBin is the compiled faketinvest binary, built once by TestMain.
var fakeBin string

func TestMain(m *testing.M) {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate repo root:", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "faketinvest-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "temp dir:", err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "faketinvest")
	build := exec.Command("go", "build", "-o", bin, "./test/faketinvest")
	build.Dir = root
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build faketinvest:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	fakeBin = bin

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	// file = <root>/internal/tinvestcli/client_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

func shippedScenario(t *testing.T, name string) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "test", "faketinvest", "scenarios", name)
}

// writeScenario materializes a scenario directory from a name→content map and
// returns its path.
func writeScenario(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newClient(t *testing.T, scenario, state string, tune func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Path:         fakeBin,
		RetryBackoff: time.Millisecond,
		Env: append(os.Environ(),
			"FAKETINVEST_SCENARIO="+scenario,
			"FAKETINVEST_STATE="+state,
		),
	}
	if tune != nil {
		tune(&cfg)
	}
	c, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return c
}

func eqDec(t *testing.T, got model.Decimal, want string) {
	t.Helper()
	if got.String() != want {
		t.Fatalf("decimal = %q, want %q", got.String(), want)
	}
}

func eqMoney(t *testing.T, m *Money, wantVal, wantCur string) {
	t.Helper()
	if m == nil {
		t.Fatalf("money is nil, want %q %q", wantVal, wantCur)
	}
	eqDec(t, m.Amount, wantVal)
	if m.Currency != wantCur {
		t.Fatalf("currency = %q, want %q", m.Currency, wantCur)
	}
}

const sberUID = "e6123145-9665-43e0-8413-cd61b8aa9b13"

// Client order ids must be UUIDs — the real tinvest CLI rejects non-UUID
// --order-id values with a usage error.
const (
	orderUUID1 = "550e8400-e29b-41d4-a716-446655440000"
	orderUUID2 = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

func TestResolveComputesSHA256(t *testing.T) {
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), nil)
	if c.Path() != fakeBin {
		t.Fatalf("Path = %q, want %q", c.Path(), fakeBin)
	}
	if len(c.SHA256()) != 64 {
		t.Fatalf("SHA256 = %q, want 64 hex chars", c.SHA256())
	}
}

func TestResolveMissingBinary(t *testing.T) {
	_, err := Resolve(Config{Path: filepath.Join(t.TempDir(), "does-not-exist")})
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("want *ResolveError, got %T: %v", err, err)
	}
}

func TestHandshakeAccept(t *testing.T) {
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), nil)
	info, err := c.Handshake(context.Background())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if info.Version != "0.1.0" || info.Contract != "1.49" || info.SchemaVersion != "0.1" || info.GoVersion != "go1.26" {
		t.Fatalf("unexpected handshake info: %+v", info)
	}
	if info.Path != fakeBin || len(info.SHA256) != 64 {
		t.Fatalf("handshake did not carry binary identity: %+v", info)
	}
	if c.Info().Version != "0.1.0" {
		t.Fatalf("Info() not recorded: %+v", c.Info())
	}
}

func TestHandshakeRejectsUnknownSchema(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"x\"\nschema_version = \"9.9\"\n",
	})
	c := newClient(t, dir, t.TempDir(), nil)
	info, err := c.Handshake(context.Background())
	var he *HandshakeError
	if !errors.As(err, &he) {
		t.Fatalf("want *HandshakeError, got %T: %v", err, err)
	}
	if info.SchemaVersion != "9.9" {
		t.Fatalf("rejected handshake should still report the observed schema: %+v", info)
	}
}

func TestHandshakeRejectsUnknownContract(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"x\"\ncontract = \"9.9\"\n",
	})
	c := newClient(t, dir, t.TempDir(), nil)
	info, err := c.Handshake(context.Background())
	var he *HandshakeError
	if !errors.As(err, &he) {
		t.Fatalf("want *HandshakeError for a contract outside the allowlist, got %T: %v", err, err)
	}
	if info.Contract != "9.9" {
		t.Fatalf("rejected handshake should still report the observed contract: %+v", info)
	}
}

// TestHostileExitCodeMismatchIsProtocolError drives a fake that reports an
// AUTH error body under a usage exit code: the adapter must reject the
// contradiction as a ProtocolError rather than trust either half.
func TestHostileExitCodeMismatchIsProtocolError(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-mismatch\"\n" +
			"[[instruments]]\nuid = \"" + sberUID + "\"\nticker = \"SBER\"\nclass_code = \"TQBR\"\n" +
			"lot = 10\ncurrency = \"rub\"\nlast_price = \"270.5\"\nlast_price_time = \"2026-07-19T10:00:00Z\"\n" +
			"[[fail]]\ncommand = \"quotes last\"\ncode = \"AUTH\"\nexit = 2\nmessage = \"mismatched exit and code\"\n",
	})
	c := newClient(t, dir, t.TempDir(), nil)
	_, err := c.QuotesLast(context.Background(), []string{"SBER@TQBR"})
	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("want *ProtocolError for exit/code mismatch, got %T: %v", err, err)
	}
}

func TestHappyScenarioDecode(t *testing.T) {
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), nil)
	ctx := context.Background()
	const account = "test-brokerage-0001"

	t.Run("instrument get", func(t *testing.T) {
		inst, err := c.InstrumentGet(ctx, "SBER@TQBR")
		if err != nil {
			t.Fatal(err)
		}
		if inst.UID != sberUID || inst.Lot != 10 || inst.Currency != "rub" {
			t.Fatalf("unexpected instrument: %+v", inst)
		}
		eqDec(t, inst.MinPriceIncrement.Amount, "0.01")
		m := inst.Model()
		if m.Lot != 10 || string(m.UID) != sberUID {
			t.Fatalf("Model() mismatch: %+v", m)
		}
		eqDec(t, m.MinPriceIncrement, "0.01")
	})

	t.Run("instruments search", func(t *testing.T) {
		hits, err := c.InstrumentsSearch(ctx, "GAZP")
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].Ticker != "GAZP" || hits[0].Lot != 10 {
			t.Fatalf("unexpected search hits: %+v", hits)
		}
	})

	t.Run("quotes last", func(t *testing.T) {
		lp, err := c.QuotesLast(ctx, []string{"SBER@TQBR", "GAZP@TQBR"})
		if err != nil {
			t.Fatal(err)
		}
		if len(lp) != 2 {
			t.Fatalf("want 2 prices, got %d", len(lp))
		}
		eqDec(t, lp[0].Price.Amount, "270.5")
		eqDec(t, lp[1].Price.Amount, "128.4")
		q := lp[0].Quote()
		eqDec(t, q.Last, "270.5")
		if string(q.InstrumentUID) != sberUID {
			t.Fatalf("quote uid = %q", q.InstrumentUID)
		}
	})

	t.Run("candles get", func(t *testing.T) {
		from := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
		to := time.Date(2026, 7, 19, 9, 20, 0, 0, time.UTC)
		cr, err := c.CandlesGet(ctx, "SBER@TQBR", model.Interval5m, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if cr.InstrumentUID != sberUID || cr.Interval != "5m" {
			t.Fatalf("unexpected candles echo: %+v", cr)
		}
		if len(cr.Candles) != 4 {
			t.Fatalf("want 4 candles, got %d", len(cr.Candles))
		}
		eqDec(t, cr.Candles[0].Open.Amount, "270")
		eqDec(t, cr.Candles[0].Close.Amount, "270.5")
		if cr.Candles[0].Volume.Int64() != 1500 || !cr.Candles[0].IsComplete {
			t.Fatalf("candle[0] = %+v", cr.Candles[0])
		}
		if cr.Candles[3].IsComplete {
			t.Fatal("candle[3] should be incomplete")
		}
		mc, err := cr.Model()
		if err != nil {
			t.Fatal(err)
		}
		if len(mc) != 4 || mc[0].Volume != 1500 || !mc[0].Complete || mc[3].Complete {
			t.Fatalf("Model() candles mismatch: %+v", mc)
		}
		if mc[0].Interval != model.Interval5m {
			t.Fatalf("interval = %q", mc[0].Interval)
		}
		eqDec(t, mc[0].Close, "270.5")
	})

	t.Run("orderbook get", func(t *testing.T) {
		ob, err := c.OrderbookGet(ctx, "SBER@TQBR", 10)
		if err != nil {
			t.Fatal(err)
		}
		if ob.Depth != 10 || len(ob.Bids) != 3 || len(ob.Asks) != 3 {
			t.Fatalf("unexpected orderbook: %+v", ob)
		}
		eqDec(t, ob.Bids[0].Price.Amount, "270.4")
		if ob.Bids[0].Quantity.Int64() != 120 {
			t.Fatalf("bid qty = %d", ob.Bids[0].Quantity.Int64())
		}
		eqDec(t, ob.Asks[0].Price.Amount, "270.6")
		q := ob.Quote()
		eqDec(t, q.Bid, "270.4")
		eqDec(t, q.Ask, "270.6")
		eqDec(t, q.Last, "270.5")
		if !q.HasBidAsk() {
			t.Fatal("quote should have bid and ask")
		}
	})

	t.Run("portfolio get", func(t *testing.T) {
		pf, err := c.PortfolioGet(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if pf.AccountID != account {
			t.Fatalf("account = %q", pf.AccountID)
		}
		eqMoney(t, &pf.TotalAmountPortfolio, "100000", "rub")
		eqDec(t, pf.ExpectedYield.Amount, "1.25")
		if len(pf.Positions) != 1 {
			t.Fatalf("want 1 position, got %d", len(pf.Positions))
		}
		p := pf.Positions[0]
		eqDec(t, p.Quantity.Amount, "100")
		eqMoney(t, &p.AveragePositionPrice, "265", "rub")
		eqMoney(t, &p.CurrentPrice, "270.5", "rub")
	})

	t.Run("positions get", func(t *testing.T) {
		pos, err := c.PositionsGet(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if len(pos.Money) != 1 {
			t.Fatalf("want 1 money row, got %d", len(pos.Money))
		}
		eqMoney(t, &pos.Money[0], "45900", "rub")
		if len(pos.Securities) != 1 || pos.Securities[0].Balance.Int64() != 100 {
			t.Fatalf("unexpected securities: %+v", pos.Securities)
		}
		if pos.Securities[0].InstrumentUID != sberUID {
			t.Fatalf("security uid = %q", pos.Securities[0].InstrumentUID)
		}
	})

	t.Run("operations list", func(t *testing.T) {
		ops, err := c.OperationsList(ctx, account, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(ops.Operations) != 1 {
			t.Fatalf("want 1 operation, got %d", len(ops.Operations))
		}
		op := ops.Operations[0]
		if op.ID != "op-1001" || op.TradeCount != 1 || op.Quantity.Int64() != 100 {
			t.Fatalf("unexpected operation: %+v", op)
		}
		eqMoney(t, &op.Payment, "-26500", "rub")
		eqMoney(t, &op.Commission, "-13.25", "rub")
	})

	t.Run("orders place", func(t *testing.T) {
		price := model.MustDecimal("270.5")
		ord, err := c.OrdersPlace(ctx, PlaceRequest{
			Account:      account,
			InstrumentID: "SBER@TQBR",
			Direction:    model.SideBuy,
			Quantity:     1,
			Type:         model.OrderLimit,
			LimitPrice:   &price,
			TimeInForce:  model.TIFDay,
			OrderID:      orderUUID1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if ord.OrderID != "ord-"+orderUUID1 || ord.ClientOrderID != orderUUID1 {
			t.Fatalf("order ids: %+v", ord)
		}
		if ord.Direction != "ORDER_DIRECTION_BUY" || ord.OrderType != "ORDER_TYPE_LIMIT" {
			t.Fatalf("order enums: %+v", ord)
		}
		if ord.Lots != (Lots{Requested: 1, Executed: 1, Remaining: 0}) {
			t.Fatalf("lots = %+v", ord.Lots)
		}
		eqMoney(t, ord.InitialPrice, "270.5", "rub")
		eqMoney(t, ord.ExecutedPrice, "270.6", "rub")
		eqMoney(t, ord.TotalAmount, "2706", "rub")
		eqMoney(t, ord.Commission, "1.35", "rub")
	})

	t.Run("orders cancel", func(t *testing.T) {
		// Cancel takes the exchange order id (not a client UUID) positionally.
		exchangeID := "ord-" + orderUUID1
		res, err := c.OrdersCancel(ctx, account, exchangeID)
		if err != nil {
			t.Fatal(err)
		}
		if res.OrderID != exchangeID {
			t.Fatalf("cancel order id = %q", res.OrderID)
		}
	})

	t.Run("orders list empty", func(t *testing.T) {
		list, err := c.OrdersList(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 0 {
			t.Fatalf("want empty orders, got %d", len(list))
		}
	})

	t.Run("stop-orders list empty", func(t *testing.T) {
		stops, err := c.StopOrdersList(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if len(stops) != 0 {
			t.Fatalf("want empty stop orders, got %d", len(stops))
		}
	})

	t.Run("orders reconcile empty", func(t *testing.T) {
		rec, err := c.OrdersReconcile(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.Outcomes) != 0 || rec.UnresolvedCount != 0 {
			t.Fatalf("unexpected reconcile: %+v", rec)
		}
	})
}

func TestHostileNetworkRetryThenSuccess(t *testing.T) {
	c := newClient(t, shippedScenario(t, "hostile"), t.TempDir(), nil)
	from := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 19, 9, 20, 0, 0, time.UTC)
	// candles get fails NETWORK on the first call and succeeds on the retry.
	cr, err := c.CandlesGet(context.Background(), "SBER@TQBR", model.Interval5m, from, to)
	if err != nil {
		t.Fatalf("candles should succeed after a network retry: %v", err)
	}
	if cr.InstrumentUID != sberUID || len(cr.Candles) == 0 {
		t.Fatalf("unexpected candles after retry: %+v", cr)
	}
}

func TestHostileOutcomeUnknownMutationNotRetried(t *testing.T) {
	c := newClient(t, shippedScenario(t, "hostile"), t.TempDir(), nil)
	price := model.MustDecimal("270.5")
	_, err := c.OrdersPlace(context.Background(), PlaceRequest{
		Account:      "test-brokerage-0002",
		InstrumentID: "SBER@TQBR",
		Direction:    model.SideBuy,
		Quantity:     1,
		Type:         model.OrderLimit,
		LimitPrice:   &price,
		TimeInForce:  model.TIFDay,
		OrderID:      orderUUID2,
	})
	var oue *OutcomeUnknownError
	if !errors.As(err, &oue) {
		t.Fatalf("want *OutcomeUnknownError, got %T: %v", err, err)
	}
	if oue.ReconcileHint.Command != "tinvest orders reconcile" {
		t.Fatalf("reconcile command = %q", oue.ReconcileHint.Command)
	}
	if oue.ReconcileHint.OrderID != orderUUID2 {
		t.Fatalf("reconcile order id = %q, want the client order id", oue.ReconcileHint.OrderID)
	}
}

// TestOrdersPlaceRequiresUUIDOrderID proves an empty or malformed order id is
// rejected as a UsageError before any subprocess is spawned: the CLI would
// otherwise mint an id the journal never sees.
func TestOrdersPlaceRequiresUUIDOrderID(t *testing.T) {
	c := newClient(t, shippedScenario(t, "happy"), t.TempDir(), nil)
	price := model.MustDecimal("270.5")
	base := PlaceRequest{
		Account: "test-brokerage-0001", InstrumentID: "SBER@TQBR", Direction: model.SideBuy,
		Quantity: 1, Type: model.OrderLimit, LimitPrice: &price, TimeInForce: model.TIFDay,
	}
	for _, id := range []string{"", "not-a-uuid", "550e8400e29b41d4a716446655440000", "550e8400-e29b-41d4-a716-44665544000g"} {
		req := base
		req.OrderID = id
		_, err := c.OrdersPlace(context.Background(), req)
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Fatalf("order id %q must fail UsageError before spawn, got %T: %v", id, err, err)
		}
	}
}

// TestMutationTimeoutIsOutcomeUnknown proves a post-spawn local kill of a
// mutation is classified as OutcomeUnknownError (the order may have reached the
// broker), carrying a reconcile hint, not a retryable NetworkError.
func TestMutationTimeoutIsOutcomeUnknown(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-timeout\"\n" +
			"default_latency_ms = 5000\n" +
			"[[instruments]]\nuid = \"" + sberUID + "\"\nticker = \"SBER\"\nclass_code = \"TQBR\"\n" +
			"lot = 10\ncurrency = \"rub\"\nlast_price = \"270.5\"\nlast_price_time = \"2026-07-19T10:00:00Z\"\n",
	})
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		cfg.Timeout = 50 * time.Millisecond
		cfg.KillGrace = 50 * time.Millisecond
	})
	price := model.MustDecimal("270.5")
	_, err := c.OrdersPlace(context.Background(), PlaceRequest{
		Account: "test-timeout", InstrumentID: "SBER@TQBR", Direction: model.SideBuy,
		Quantity: 1, Type: model.OrderLimit, LimitPrice: &price, TimeInForce: model.TIFDay,
		OrderID: orderUUID1,
	})
	var oue *OutcomeUnknownError
	if !errors.As(err, &oue) {
		t.Fatalf("post-spawn mutation kill must be *OutcomeUnknownError, got %T: %v", err, err)
	}
	if oue.ReconcileHint.OrderID != orderUUID1 || oue.ReconcileHint.Command != "tinvest orders reconcile" {
		t.Fatalf("reconcile hint = %+v, want the client order id and reconcile command", oue.ReconcileHint)
	}
}

// TestReadTimeoutIsNetworkError proves a post-spawn local kill of a read call
// stays a transient NetworkError (retryable), unlike a mutation.
func TestReadTimeoutIsNetworkError(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-timeout\"\n" +
			"default_latency_ms = 5000\n" +
			"[[instruments]]\nuid = \"" + sberUID + "\"\nticker = \"SBER\"\nclass_code = \"TQBR\"\n" +
			"lot = 10\ncurrency = \"rub\"\nlast_price = \"270.5\"\nlast_price_time = \"2026-07-19T10:00:00Z\"\n",
	})
	c := newClient(t, dir, t.TempDir(), func(cfg *Config) {
		cfg.Timeout = 50 * time.Millisecond
		cfg.KillGrace = 50 * time.Millisecond
		cfg.Retries = -1 // disable retries so the first timeout surfaces
	})
	_, err := c.QuotesLast(context.Background(), []string{"SBER@TQBR"})
	var ne *NetworkError
	if !errors.As(err, &ne) || !ne.Timeout {
		t.Fatalf("read timeout must be a *NetworkError with Timeout=true, got %T: %v", err, err)
	}
}

func TestHostileRateLimitHonoredAndCapped(t *testing.T) {
	c := newClient(t, shippedScenario(t, "hostile"), t.TempDir(), func(cfg *Config) {
		cfg.RetryAfterCap = 150 * time.Millisecond // cap the 1500ms hint for a fast test
	})
	ctx := context.Background()
	// First quotes call succeeds; the second is rate limited (exit 4) and must be
	// retried only after honoring retry_after, capped at RetryAfterCap.
	if _, err := c.QuotesLast(ctx, []string{"SBER@TQBR"}); err != nil {
		t.Fatalf("first quotes: %v", err)
	}
	start := time.Now()
	lp, err := c.QuotesLast(ctx, []string{"SBER@TQBR"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("rate-limited quotes should succeed on retry: %v", err)
	}
	if len(lp) != 1 {
		t.Fatalf("want 1 price, got %d", len(lp))
	}
	eqDec(t, lp[0].Price.Amount, "270.5")
	if elapsed < 120*time.Millisecond {
		t.Fatalf("retry did not honor retry_after: waited only %v", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("retry_after was not capped: waited %v", elapsed)
	}
}

func TestReconcileUnresolvedExit1IsSuccess(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml":         "account_id = \"test-reconcile\"\n[responses]\norders_reconcile = \"orders_reconcile.json\"\n",
		"orders_reconcile.json": `{"outcomes":[{"intent_id":"i1","client_order_id":"` + orderUUID1 + `","account_id":"test-reconcile","outcome":"unknown"}],"unresolved_count":2}`,
	})
	c := newClient(t, dir, t.TempDir(), nil)
	rec, err := c.OrdersReconcile(context.Background(), "test-reconcile")
	if err != nil {
		t.Fatalf("reconcile with unresolved intents should be success-with-unresolved: %v", err)
	}
	if rec.UnresolvedCount != 2 {
		t.Fatalf("UnresolvedCount = %d, want 2", rec.UnresolvedCount)
	}
	if len(rec.Outcomes) != 1 || rec.Outcomes[0].ClientOrderID != orderUUID1 {
		t.Fatalf("unexpected outcomes: %+v", rec.Outcomes)
	}
}

// TestReconcileCarriesErrorAndNote proves the adapter preserves the error and
// note fields the real reconcile renderer emits, rather than dropping them.
func TestReconcileCarriesErrorAndNote(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-reconcile\"\n[responses]\norders_reconcile = \"orders_reconcile.json\"\n",
		"orders_reconcile.json": `{"outcomes":[{"intent_id":"i1","client_order_id":"` + orderUUID1 + `","account_id":"test-reconcile",` +
			`"outcome":"placed","order_id":"ord-1","lifecycle":"EXECUTION_REPORT_STATUS_FILL",` +
			`"error":"ledger write failed","note":"match is heuristic (no client-id correlation)"}],"unresolved_count":0}`,
	})
	c := newClient(t, dir, t.TempDir(), nil)
	rec, err := c.OrdersReconcile(context.Background(), "test-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Outcomes) != 1 {
		t.Fatalf("want 1 outcome, got %d", len(rec.Outcomes))
	}
	o := rec.Outcomes[0]
	if o.Error != "ledger write failed" {
		t.Fatalf("error = %q, want the reconcile error surfaced", o.Error)
	}
	if o.Note != "match is heuristic (no client-id correlation)" {
		t.Fatalf("note = %q, want the reconcile note surfaced", o.Note)
	}
}

// TestStopOrdersStatusDecodes proves a stop order's state decodes from the
// renderer's status field (not the order-lifecycle field an order uses).
func TestStopOrdersStatusDecodes(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-stops\"\n[responses]\nstop_orders = \"stop_orders.json\"\n",
		"stop_orders.json": `{"stop_orders":[{"stop_order_id":"so-1","status":"STOP_ORDER_STATUS_ACTIVE",` +
			`"direction":"STOP_ORDER_DIRECTION_SELL","stop_order_type":"STOP_ORDER_TYPE_STOP_LOSS",` +
			`"instrument_uid":"u1","ticker":"SBER","currency":"rub",` +
			`"stop_price":{"units":"260","nano":0,"value":"260","currency":"rub"}}]}`,
	})
	c := newClient(t, dir, t.TempDir(), nil)
	stops, err := c.StopOrdersList(context.Background(), "test-stops")
	if err != nil {
		t.Fatal(err)
	}
	if len(stops) != 1 {
		t.Fatalf("want 1 stop order, got %d", len(stops))
	}
	s := stops[0]
	if s.Status != "STOP_ORDER_STATUS_ACTIVE" {
		t.Fatalf("status = %q, want it decoded from the status field", s.Status)
	}
	if s.StopOrderType != "STOP_ORDER_TYPE_STOP_LOSS" {
		t.Fatalf("stop_order_type = %q", s.StopOrderType)
	}
	eqMoney(t, s.StopPrice, "260", "rub")
}

func TestOrdersGetAndListDecode(t *testing.T) {
	// A fetched order's full state reports executed_commission (not the
	// placement view's initial_commission) plus currency and order_date.
	order := `{"order_id":"ord-1","client_order_id":"` + orderUUID1 + `","lifecycle":"EXECUTION_REPORT_STATUS_FILL",` +
		`"direction":"ORDER_DIRECTION_BUY","order_type":"ORDER_TYPE_LIMIT",` +
		`"lots":{"requested":2,"executed":2,"remaining":0},"instrument_uid":"u1","ticker":"SBER","currency":"rub",` +
		`"executed_order_price":{"units":"270","nano":600000000,"value":"270.6","currency":"rub"},` +
		`"executed_commission":{"units":"1","nano":350000000,"value":"1.35","currency":"rub"},` +
		`"order_date":"2026-07-19T10:00:05Z"}`
	dir := writeScenario(t, map[string]string{
		"scenario.toml":    "account_id = \"test-orders\"\n[responses]\norders_get = \"orders_get.json\"\norders_list = \"orders_list.json\"\n",
		"orders_get.json":  `{"order":` + order + `}`,
		"orders_list.json": `{"orders":[` + order + `]}`,
	})
	c := newClient(t, dir, t.TempDir(), nil)
	ctx := context.Background()

	ord, err := c.OrdersGet(ctx, "test-orders", "ord-1")
	if err != nil {
		t.Fatal(err)
	}
	if ord.OrderID != "ord-1" || ord.Lots != (Lots{Requested: 2, Executed: 2, Remaining: 0}) {
		t.Fatalf("orders get: %+v", ord)
	}
	if ord.Currency != "rub" {
		t.Fatalf("currency = %q, want rub", ord.Currency)
	}
	eqMoney(t, ord.ExecutedPrice, "270.6", "rub")
	eqMoney(t, ord.Commission, "1.35", "rub") // executed_commission, not initial
	if !ord.OrderDate.Equal(time.Date(2026, 7, 19, 10, 0, 5, 0, time.UTC)) {
		t.Fatalf("order_date = %v", ord.OrderDate)
	}

	list, err := c.OrdersList(ctx, "test-orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].OrderID != "ord-1" {
		t.Fatalf("orders list: %+v", list)
	}
	eqMoney(t, list[0].Commission, "1.35", "rub")
}

// TestOperationsServiceSerialized proves portfolio and operations-list calls
// serialize under one method-group lock: they share the real broker's
// OperationsService rate budget, so with a per-call fake latency the two must
// run one at a time (total >= two latencies), never concurrently.
func TestOperationsServiceSerialized(t *testing.T) {
	const latency = 150 * time.Millisecond
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-ops\"\n" +
			"default_latency_ms = 150\n" +
			"[responses]\nportfolio = \"portfolio.json\"\noperations = \"operations.json\"\n",
		"portfolio.json":  `{"account_id":"test-ops","total_amount_portfolio":{"value":"1","currency":"rub"},"positions":[]}`,
		"operations.json": `{"operations":[],"next_cursor":""}`,
	})
	c := newClient(t, dir, t.TempDir(), nil)
	ctx := context.Background()

	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, errs[0] = c.PortfolioGet(ctx, "test-ops") }()
	go func() { defer wg.Done(); _, errs[1] = c.OperationsList(ctx, "test-ops", "", 0) }()
	wg.Wait()
	elapsed := time.Since(start)

	for i, e := range errs {
		if e != nil {
			t.Fatalf("call %d: %v", i, e)
		}
	}
	if elapsed < 2*latency-30*time.Millisecond {
		t.Fatalf("operations-service calls not serialized: elapsed %v, want >= ~%v", elapsed, 2*latency)
	}
}

// TestSameGroupSerialization proves two concurrent calls in one method group run
// sequentially: with a per-call fake latency, the serialized total is at least
// two latencies, not one.
func TestSameGroupSerialization(t *testing.T) {
	const latency = 150 * time.Millisecond
	dir := writeScenario(t, map[string]string{
		"scenario.toml": "account_id = \"test-serial\"\n" +
			"default_latency_ms = 150\n" +
			"[[instruments]]\nuid = \"u-sber\"\nticker = \"SBER\"\nclass_code = \"TQBR\"\n" +
			"last_price = \"270.5\"\nlast_price_time = \"2026-07-19T10:00:00Z\"\n",
	})
	c := newClient(t, dir, t.TempDir(), nil)
	ctx := context.Background()

	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.QuotesLast(ctx, []string{"SBER@TQBR"})
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, e := range errs {
		if e != nil {
			t.Fatalf("call %d: %v", i, e)
		}
	}
	if elapsed < 2*latency-30*time.Millisecond {
		t.Fatalf("same-group calls not serialized: elapsed %v, want >= ~%v", elapsed, 2*latency)
	}
}
