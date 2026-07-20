# invest-robot

A personal autonomous trading platform: one Go binary (`robot`) that hosts a
terminal UI and an autonomous decision engine in a single process. All broker
and market access goes through the `tinvest` CLI as a subprocess — the robot
contains no T-Bank API client of its own. State lives in one SQLite file,
configuration is one TOML file. Zero infrastructure, local-first, paper-first.

Paper trading is the permanent default: the robot collects real market data but
simulates fills internally, so no order ever leaves the machine unless real mode
is explicitly enabled behind several independent gates (Phase 1 refuses real
mode entirely).

## Requirements

- Go 1.26+
- The `tinvest` CLI on `$PATH` (or pointed to via `[tinvest].path` in the
  config). The robot version-checks it at startup and refuses to start on a
  contract/schema mismatch.
- No C toolchain: the SQLite driver is pure Go.

The broker token is never stored in the robot config — `tinvest` resolves it
itself (environment, token file, or profile).

## Quickstart

```bash
go mod download
make build                       # produces bin/robot
cp robot.example.toml robot.toml # edit universe / schedule / risk
make run                         # or: bin/robot --headless
```

`make test`, `make vet`, and `make fmt-check` run the checks used in
development. Everything is offline-testable — `make test` needs no network and
no token.

### Try it against the fake broker

`scripts/dev-fake-run.sh` runs the robot headless against `faketinvest`, an
offline test double of the tinvest CLI, on a clean checkout with no token:

```bash
./scripts/dev-fake-run.sh    # Ctrl-C to stop
```

It builds the binaries, writes a throwaway config wired to the fake (the happy
scenario), and logs the startup handshake, cycle activity and stream health.

## Configuration

A single TOML file, loaded with strict decoding (unknown keys are a startup
error). Default location is `invest-robot/robot.toml` under `$XDG_CONFIG_HOME`
(or `~/.config` when unset); override with `--config`. See
`robot.example.toml` for every section and default. The main sections:

- `[tinvest]` — binary path, profile, account
- `[universe]` — instruments to watch and trade (uid / FIGI / `TICKER@CLASSCODE`)
- `[schedule]` — decision interval and trading-session window (with timezone)
- `[engine]` — decision engine (`rules` in Phase 1)
- `[risk]` — position/exposure/order/loss limits and the cash floor
- `[paper]` — starting cash, slippage, commission
- `[storage]` — SQLite database path

## Running

```
robot [--config PATH] [--headless] [--version]
```

- `--headless` runs the engine with log output and no UI.
- default mode runs the terminal UI (Bubble Tea; needs a real terminal).
- `--version` prints the robot version and the resolved tinvest handshake.

## Architecture

One process runs a Bubble Tea UI and the autonomous loop; the UI is a read-only
client of engine state, never the owner of the loop (`--headless` runs the same
core without it). The decision cycle is a serialized state machine aligned to
completed candle boundaries:

```
assemble → decide → validate → risk-check → execute → account → report
```

Every step persists to SQLite so a cycle is replayable. The engine reads
strictly as of the cycle's watermark (completed candles only), so a decision
never sees data from after the instant it was made.

```
cmd/robot                   entrypoint: flags, config load, app start
internal/app                lifecycle owner: wiring, supervision, shutdown order
internal/config             strict TOML load/validate
internal/model              domain types: fixed-point Decimal money, instruments,
                            candles, quotes, orders (intent state machine), positions
internal/clock              Clock interface (real + simulated) — no time.Now below app
internal/store/sqlite       WAL SQLite, embedded migrations, repositories
internal/tinvestcli         the only package that knows tinvest exists: resolve +
                            version handshake, exec, envelope parsing, exit-code
                            mapping, marketdata stream supervision
internal/market             market-data collector: universe resolve, candle
                            backfill, stream ingest, quotes, gap backfill, health
internal/features           pure indicators (SMA/EMA/RSI/ATR) + feature snapshots
internal/decision           decision engine port + request/response + validation
internal/decision/rules     deterministic trend/momentum rules engine
internal/risk               pre-trade limit enforcement (fail-closed)
internal/execution          Executor port + order-intent journal
internal/execution/paper    paper fill simulator (next-observation fills)
internal/portfolio          transactional cash/positions/PnL/equity
internal/cycle              the autonomous loop: scheduler + cycle state machine
internal/tui                Bubble Tea UI: dashboard, positions, decisions, orders, log
test/faketinvest            offline test double of the tinvest CLI
```

## Safety posture

- **Paper is the default and cannot be skipped.** `mode = "real"` (or
  `real.enable = true`) is refused in Phase 1; the robot never places a real
  order.
- **Risk fails closed.** `internal/risk` applies the configured limits (per-
  instrument notional, total exposure, orders per cycle/day, daily-loss kill
  switch, allowlist, cash floor) after every engine decision — an engine can
  tighten limits but never widen them, and anything it cannot prove safe is
  stripped. The per-day order cap is read from the durable journal, so a restart
  cannot reset it.
- **Kill switch.** Engaging it latches a durable operational halt (flatten-only):
  risk strips every new buy until an operator clears it; exits still pass. A fill
  that settles cash below the floor latches the same halt automatically.
- **Fail-closed startup.** The tinvest version handshake, universe resolution,
  and order-journal reconciliation all abort or degrade safely rather than
  proceed on bad state.
- **Money is exact.** Fixed-point decimals end to end; floats appear only inside
  indicator math, never in cash or position bookkeeping.

## Testing

`make test` runs everything offline. Integration tests drive the full paper
cycle against `faketinvest` (envelope-, exit-code- and stream-faithful) with a
simulated clock, including a determinism check that identical inputs reproduce
byte-identical decisions.

## Status

Phase 1 is runnable: config, SQLite storage, the tinvest CLI adapter (unary +
marketdata stream), the market collector, features, the rules engine, risk, the
paper executor, portfolio accounting, the autonomous cycle, and the terminal UI.
Later phases add LLM decision engines, richer data, backtesting, and the real-
trading safety ladder.
