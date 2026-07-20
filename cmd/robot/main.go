// Command robot is the invest-robot binary: a Bubble Tea TUI and an
// autonomous decision engine in a single process, talking to the broker
// only through the tinvest CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Dronnn/invest-robot/internal/app"
	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
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
		printVersion(*configPath)
		return 0
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "robot: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.New(cfg, *headless).Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "robot: %v\n", err)
		return 1
	}
	return 0
}

// printVersion prints the robot version and, best-effort, the resolved tinvest
// CLI handshake info (DESIGN §4: the version check is part of the contract).
func printVersion(configPath string) {
	fmt.Println("robot", version)
	cfg, err := config.Load(configPath)
	tinvestPath := ""
	if err == nil {
		tinvestPath = cfg.TInvest.Path
	}
	client, err := tinvestcli.Resolve(tinvestcli.Config{Path: tinvestPath, Env: os.Environ()})
	if err != nil {
		fmt.Printf("tinvest: not resolved (%v)\n", err)
		return
	}
	info, err := client.Handshake(context.Background())
	if err != nil {
		fmt.Printf("tinvest: handshake failed (%v)\n", err)
		return
	}
	fmt.Printf("tinvest: %s (contract %s, schema %s)\n  path: %s\n  sha256: %s\n",
		info.Version, info.Contract, info.SchemaVersion, info.Path, info.SHA256)
}

func defaultConfigPath() string {
	dir, err := config.DefaultDir()
	if err != nil {
		return "robot.toml"
	}
	return filepath.Join(dir, "robot.toml")
}
