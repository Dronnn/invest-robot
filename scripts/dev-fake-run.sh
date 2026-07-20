#!/usr/bin/env bash
#
# dev-fake-run.sh runs the robot headless against the faketinvest test double, so
# the full loop can be exercised on a clean checkout with no broker token.
#
#   ./scripts/dev-fake-run.sh
#
# It builds faketinvest, writes a throwaway robot.toml wired to it (happy
# scenario), and runs `robot --headless`, which logs cycle activity and stream
# health. Ctrl-C to stop. With the minimal happy fixture the indicators do not
# fully warm up, so cycles log as skipped until enough bars exist — the point is
# to prove the process starts, handshakes tinvest, collects, and loops.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fake="$work/faketinvest"
robot="$work/robot"
go build -o "$fake" ./test/faketinvest
go build -o "$robot" ./cmd/robot

scenario="$root/test/faketinvest/scenarios/happy"
config="$work/robot.toml"
cat > "$config" <<EOF
mode = "paper"

[tinvest]
path = "$fake"

[universe]
instruments = ["SBER@TQBR", "GAZP@TQBR"]

[schedule]
interval = "5m"
session_start = ""
session_end = ""
timezone = "UTC"

[engine]
active = "rules"

[risk]
max_position_notional = "50000"
max_total_exposure = "150000"
max_orders_per_cycle = 5
max_orders_per_day = 20
max_daily_loss = "5000"
cash_floor = "10000"

[storage]
db_path = "$work/robot.db"

[paper]
starting_cash = "100000"
slippage_bps = 5
commission_rate = "0.0004"
EOF

export FAKETINVEST_SCENARIO="$scenario"
export FAKETINVEST_STATE="$work/state"
mkdir -p "$FAKETINVEST_STATE"

echo "dev-fake-run: robot --headless --config $config"
exec "$robot" --headless --config "$config"
