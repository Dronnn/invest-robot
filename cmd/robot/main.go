// Command robot is the invest-robot binary: a Bubble Tea TUI and an
// autonomous decision engine in a single process, talking to the broker
// only through the tinvest CLI.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dronnn/invest-robot/internal/config"
)

// version is the robot build version. Overridden at release build time via
// -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("robot", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to robot.toml")
	headless := fs.Bool("headless", false, "run without the TUI")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Println("robot", version)
		return 0
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "robot: %v\n", err)
		return 1
	}

	fmt.Printf("robot %s: mode=%s engine=%s universe=%d instruments headless=%v db=%s\n",
		version, cfg.Mode, cfg.Engine.Active, len(cfg.Universe.Instruments), *headless, cfg.Storage.DBPath)

	// App wiring (storage, tinvestcli, collectors, decision cycle, TUI)
	// lands in later steps; Phase 1 scaffold stops after a successful
	// config load.
	return 0
}

func defaultConfigPath() string {
	dir, err := config.DefaultDir()
	if err != nil {
		return "robot.toml"
	}
	return filepath.Join(dir, "robot.toml")
}
