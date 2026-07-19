# faketinvest — a test double for the `tinvest` CLI

`faketinvest` is a small `main` package that impersonates the real `tinvest`
CLI faithfully enough to drive the invest-robot's integration tests offline: no
network, no broker token. It speaks the same JSON envelope, the same exit-code
contract, and the same NDJSON stream framing, and its behavior is chosen
entirely by a **scenario directory** so it is fully deterministic.

The contract it mirrors lives in the sibling `tinvest` repo
(`internal/render/*.go` and `AGENTS.md`): envelope `schema_version = "0.1"`,
`contract = "1.49"`, decimal-string money (`{units, nano, value}`), UTC RFC 3339
timestamps, and exit codes `0..7`.

## How the robot invokes it

The robot resolves the broker CLI through `tinvest.path` in its config (§4 of
`docs/DESIGN.md`), so a test points that at the built `faketinvest` binary and
selects a scenario with an environment variable:

```
tinvest.path = "/abs/path/to/faketinvest"     # in the robot's test config
FAKETINVEST_SCENARIO = "/abs/path/to/scenario" # env var the fake reads
```

The fake is invoked with the *real* tinvest argv (`version -o json`,
`quotes last SBER@TQBR -o json`, `stream marketdata --instrument ... -o json`,
…) and answers from the scenario. A `--scenario <dir>` flag is also accepted for
manual runs. With no scenario configured, unauthenticated commands still work
(`faketinvest version -o json`) using built-in contract defaults.

Set `FAKETINVEST_STATE` to a writable directory (a temp dir in tests) so the
persistent call counter used by failure injection is written there instead of
into the committed scenario directory.

## Commands spoken

`version`; `instruments get|search`; `quotes last` (multiple instruments);
`candles get`; `orderbook get`; `portfolio get`; `positions get`;
`operations list`; `orders place|get|cancel|list|reconcile`; `stop-orders list`;
`stream marketdata`. `-o json` is always honored (and an invalid `-o` value is a
USAGE error, exit 2, exactly like the real CLI). The fake emits JSON regardless
of `-o table` — it has no human table renderer.

## Scenario format

A scenario is a directory containing `scenario.toml` (or `scenario.json`) plus
JSON fixture files. See `scenarios/happy` and `scenarios/hostile` for complete
worked examples.

### `scenario.toml`

```toml
account_id = "test-brokerage-0001"   # meta.account_id on account-scoped commands
schema_version = "0.1"                # envelope schema_version (default "0.1")
contract = "1.49"                     # meta.contract (default "1.49")
version = "0.1.0"                     # `version` data.version (handshake tests)
go_version = "go1.26"                 # `version` data.go
default_latency_ms = 0               # sleep before a unary reply; also meta.elapsed_ms

# The instrument universe. Reference fields mirror render.InstrumentView; the
# market fields drive quotes/orderbook/candles for that instrument. An id is
# resolved by uid, figi, or TICKER@CLASSCODE.
[[instruments]]
uid = "e6123145-9665-43e0-8413-cd61b8aa9b13"
figi = "BBG004730N88"
ticker = "SBER"
class_code = "TQBR"
name = "Sberbank"
type = "INSTRUMENT_TYPE_SHARE"
lot = 10
currency = "rub"
min_price_increment = "0.01"
trading_status = "SECURITY_TRADING_STATUS_NORMAL_TRADING"
last_price = "270.5"                  # -> quotes last
last_price_time = "2026-07-19T10:00:00Z"
price_type = "LAST_PRICE_EXCHANGE"
orderbook = "orderbook/SBER.json"     # fixture -> OrderBookView (orderbook get)
candles = "candles/SBER.json"         # fixture -> []CandleView (candles get)

# Account-level fixtures emitted verbatim as the envelope `data`. Each file is
# the exact data object, including its wrapper key.
[responses]
portfolio = "portfolio.json"          # {"portfolio": {...}}
positions = "positions.json"          # {"positions": {...}}
operations = "operations.json"        # {"operations": [...], "next_cursor": ""}
orders_list = "orders_list.json"      # {"orders": [...]}
orders_reconcile = "orders_reconcile.json"  # {"outcomes": [...], "unresolved_count": N}
stop_orders = "stop_orders.json"      # {"stop_orders": [...]}
instruments_search = "search.json"    # optional; otherwise the universe is filtered

# `orders place` is synthesized from the request flags plus these defaults. The
# client order id from --order-id is always echoed back as client_order_id.
[orders]
order_id_prefix = "ord-"              # exchange order_id = prefix + client order id
[orders.place]
lifecycle = "EXECUTION_REPORT_STATUS_FILL"
executed_price = "270.6"              # optional money fields, currency below
total_amount = "2706"
commission = "1.35"
currency = "rub"

# One or more failure-injection rules. A rule fires on the OnCall-th matching
# invocation (1-based); OnCall <= 0 fires on every matching call. Call counts
# persist across process spawns via the state directory.
[[fail]]
command = "quotes last"               # the command key to match
on_call = 2
code = "RATE_LIMITED"                 # USAGE/POLICY->2 AUTH->3 RATE_LIMITED->4
                                      # BROKER_REJECTED->5 NETWORK->6 UNCONFIRMED->7 INTERNAL->1
message = "unary rate limit exceeded"
retryable = true
retry_after_ms = 1500
# exit = 4                            # optional explicit exit override
# reconcile_command = "tinvest orders reconcile"  # for UNCONFIRMED; order_id defaults to --order-id

[stream]
script = "stream/marketdata.json"     # ordered stream script (see below)
shutdown_time = "2026-07-19T10:05:00Z" # timestamp on the shutdown frame
```

### Stream script

`stream marketdata` plays an ordered script — a JSON array (or NDJSON) of frame
entries. Each entry becomes one NDJSON line `{type, schema_version, time,
account_id?, data?, error?}`. Lifecycle fields (`attempt`, `subscriptions`,
`reason`, `final`) go inside `data`, matching the real `LifecycleView`.

```json
[
  { "type": "connected", "time": "...", "data": { "attempt": 1, "subscriptions": 2 } },
  { "type": "candle", "time": "...", "data": { /* StreamCandleView */ } },
  { "type": "last_price", "time": "...", "data": { /* LastPriceView */ } },
  { "type": "disconnected", "time": "...", "data": { "attempt": 1, "subscriptions": 1, "reason": "upstream reset" } },
  { "type": "candle", "time": "...", "data": { /* a later candle, leaving a gap */ } },
  { "exit": 0 }
]
```

- `delay_ms` on an entry paces emission (default 0 — fast for tests).
- An entry with `exit` terminates the process with that code after emitting its
  frame, simulating a mid-stream process death for the robot's stream supervisor
  to restart.
- On `SIGINT`/`SIGTERM` (or context cancellation) the fake emits a final
  `disconnected` frame with `data.reason = "shutdown"`, `data.final = true` and
  exits 0 — the real CLI's clean-shutdown signal.

## Shipped scenarios

- **`scenarios/happy`** — SBER and GAZP, stable quotes/candles/orderbook, a
  portfolio with one position, and `orders place` that fills cleanly. No
  injected failures. A finite market-data stream (connected, candles, last
  prices, EOF).
- **`scenarios/hostile`** — a rate limit on the second `quotes last` (exit 4,
  `retry_after_ms`), a network failure on the first `candles get` (exit 6), an
  outcome-unknown first `orders place` (exit 7 with a reconcile hint) that
  `orders reconcile` later resolves cleanly, and a market-data stream that
  disconnects in-band, resumes with a candle gap, and then exits so the
  supervisor must restart it.

## Tests

`go test ./test/faketinvest/` exercises the fake two ways:

1. **In-process** via the testable `run(ctx, argv, env, stdout, stderr) int`
   entry point (no `os.Exit`): golden envelope output per command
   (`testdata/*.json`, regenerate with `-update`), exit-code injection, usage
   and rejection paths, and stream playback / graceful shutdown.
2. **Built binary** (`go build` into a temp dir) for the end-to-end SIGTERM
   shutdown, which also covers `main.go`'s signal wiring and the built-binary
   invocation path that integration tests use.

Regenerate goldens after an intentional format change:

```
go test ./test/faketinvest/ -run TestGoldenEnvelopes -update
```
