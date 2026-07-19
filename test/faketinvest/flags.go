package main

import "strings"

// persistentFlags are the root-level flags accepted by every command, mirroring
// cmd/tinvest's rootCmd persistent flag set (the sibling tinvest repo's
// cmd/tinvest/root.go rootCmd func: --profile, --account, --output/-o,
// --token-file, --timeout, --sandbox, --no-rate-limit).
var persistentFlags = map[string]bool{
	"--profile": true, "--account": true, "--output": true, "-o": true,
	"--token-file": true, "--timeout": true, "--sandbox": true, "--no-rate-limit": true,
}

// commandFlags is the local (non-persistent) flag surface of every command the
// fake speaks, mirroring each command's cmd.Flags() registration in the sibling
// tinvest repo's cmd/tinvest/*.go. A command present here with an empty set
// takes no local flags. A command NOT present here is unknown to the flag
// registry; validateFlags leaves it to the normal command dispatch (buildUnary
// / run's switch) to report "unknown command", preserving that message.
var commandFlags = map[string]map[string]bool{
	"version":            {},
	"instruments get":    {"--no-cache": true},
	"instruments search": {},
	"quotes last":        {"--no-cache": true},
	"candles get":        {"--interval": true, "--from": true, "--to": true, "--no-cache": true},
	"orderbook get":      {"--depth": true, "--no-cache": true},
	"portfolio get":      {},
	"positions get":      {},
	"operations list":    {"--from": true, "--to": true, "--instrument": true, "--cursor": true, "--all": true},
	"orders place": {
		"--instrument": true, "--direction": true, "--quantity": true, "--type": true,
		"--price": true, "--tif": true, "--order-id": true, "--async": true,
		"--confirm-margin-trade": true, "--dry-run": true, "--yes": true,
		"--input": true, "--no-cache": true,
	},
	"orders get":       {"--request-id": true},
	"orders list":      {},
	"orders cancel":    {},
	"orders reconcile": {},
	"stop-orders list": {"--status": true},
	"stream marketdata": {
		"--instrument": true, "--candles": true, "--orderbook": true,
		"--trades": true, "--last-price": true, "--info": true,
	},
}

// validateFlags rejects a flag that cobra would reject: one not in the
// persistent set nor the resolved command's local set. It walks the original
// argv (not the parsed flag map) so the reported flag is the first offending
// one in invocation order, matching pflag's fail-fast parse. Commands the
// registry doesn't know about are left to buildUnary's own "unknown command"
// handling — this function only judges flags for a recognized command.
func validateFlags(argv []string, command string) *errorBody {
	local, known := commandFlags[command]
	if !known {
		return nil
	}
	for _, tok := range argv {
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			continue
		}
		name, _, _ := strings.Cut(tok, "=")
		if persistentFlags[name] || local[name] {
			continue
		}
		return &errorBody{Code: "USAGE", Message: "unknown flag: " + name}
	}
	return nil
}
