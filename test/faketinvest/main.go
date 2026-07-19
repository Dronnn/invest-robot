// Command faketinvest is a scenario-driven test double for the real `tinvest`
// CLI. It speaks the same JSON envelope, exit-code contract, and NDJSON stream
// framing so integration tests can drive the invest-robot offline, with no
// network and no broker token. Behavior is chosen entirely by a scenario
// directory (the --scenario flag or the FAKETINVEST_SCENARIO env var); the fake
// is otherwise deterministic. See README.md for the scenario format.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// A cancelled context (SIGINT/SIGTERM) drives the graceful stream shutdown
	// path, mirroring the robot's cancel -> SIGTERM -> grace shutdown of its
	// stream child.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
