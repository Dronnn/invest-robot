# invest-robot

A personal autonomous trading platform: one Go binary (`robot`) that hosts a
terminal UI and an autonomous decision engine in a single process. All
broker and market access goes through the `tinvest` CLI as a subprocess —
the robot contains no T-Bank API client of its own. State lives in one
SQLite file; configuration is one TOML file. Zero infrastructure,
local-first.

## Requirements

- Go 1.26+
- `tinvest` CLI available on `$PATH` (or pointed to via config)

## Quickstart

```bash
go mod download
cp robot.example.toml robot.toml   # edit as needed
make run
```

`make build` produces `bin/robot`. `make test`, `make vet`, and
`make fmt-check` run the checks used in development.

## Configuration

Config is a single TOML file, loaded with strict decoding (unknown keys are
a startup error). Default location is `invest-robot/robot.toml` under
`$XDG_CONFIG_HOME` (or `~/.config` when that is unset) on all platforms,
matching the sibling `tinvest` CLI; overridable with `--config`. See
`robot.example.toml` for all sections and defaults.

The broker token is never stored in this config — `tinvest` resolves it
itself (env, token file, or profile).

## Project layout

```
cmd/robot         entrypoint: flags, config load, app start
internal/app      lifecycle owner, wiring, supervision
internal/config   strict TOML config load/validate
internal/model    core domain types: fixed-point Decimal money, instruments,
                  candles, quotes, orders (intent state machine), positions
internal/clock    Clock interface with real and simulated implementations
test/faketinvest  offline test double of the tinvest CLI (envelope-, exit-
                  code- and stream-faithful, scenario-driven)
```

Remaining packages (storage, market data collection, features, decision
engines, risk, execution, portfolio, TUI) arrive with each implementation
phase.

## Design in short

- All broker/market access is a `tinvest` subprocess: JSON envelope with a
  version handshake at startup, typed exit-code mapping, order intents
  journaled before every mutation, reconciliation on unknown outcomes.
- One SQLite file (pure-Go driver), embedded migrations, decimal-string
  money end to end.
- Decision engines are pluggable (deterministic rules, LLM-backed later);
  every cycle's full context and decision is persisted for replay.
- Paper trading is the permanent default; the paper broker simulates fills
  internally against real market data. Real trading stays behind multiple
  explicit, independent gates and is never a default.

## Status

Phase 1 — skeleton, SQLite storage, market data collection, paper trading
loop, TUI — in progress.
