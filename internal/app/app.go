// Package app owns the robot's root lifecycle: it will wire together
// storage, the tinvest CLI, collectors, the decision cycle, and the TUI.
// Later steps flesh this out; for now it only proves the wiring compiles
// and runs end to end.
package app

import (
	"context"
	"log"

	"github.com/Dronnn/invest-robot/internal/config"
)

// App is the lifecycle owner for the robot process.
type App struct {
	cfg *config.Config
}

// New builds an App from a loaded, validated config.
func New(cfg *config.Config) *App {
	// Log timestamps in UTC to match the robot's UTC-everywhere discipline
	// (DESIGN §3), so event logs line up with the UTC times stored in SQLite.
	log.SetFlags(log.LstdFlags | log.LUTC)
	return &App{cfg: cfg}
}

// Run starts the robot and blocks until ctx is canceled or a fatal error
// occurs. Background services (collectors, the decision cycle, the TUI)
// are supervised from here in later steps.
func (a *App) Run(ctx context.Context) error {
	log.Printf("app: not yet implemented (mode=%s engine=%s)", a.cfg.Mode, a.cfg.Engine.Active)
	<-ctx.Done()
	return ctx.Err()
}
