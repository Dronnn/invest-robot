// Package app owns the robot's root lifecycle (DESIGN §3): it wires storage, the
// tinvest CLI, the market collector, the decision cycle, and the TUI, supervises
// them, and tears them down in order. The TUI is a client of app state, never
// the owner of the loop; --headless runs the same core without it.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/config"
	"github.com/Dronnn/invest-robot/internal/cycle"
	"github.com/Dronnn/invest-robot/internal/decision/rules"
	"github.com/Dronnn/invest-robot/internal/execution"
	"github.com/Dronnn/invest-robot/internal/execution/paper"
	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/market"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/portfolio"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
	"github.com/Dronnn/invest-robot/internal/tui"
)

// baseCurrency is the account settlement currency. Phase 1 is single-account,
// single-currency T-Bank (DESIGN §14); there is no config field for it.
const baseCurrency = "rub"

// App is the lifecycle owner for the robot process.
type App struct {
	cfg      *config.Config
	headless bool
}

// New builds an App from a loaded, validated config.
func New(cfg *config.Config, headless bool) *App {
	// UTC log timestamps match the robot's UTC-everywhere discipline (DESIGN §3).
	log.SetFlags(log.LstdFlags | log.LUTC)
	return &App{cfg: cfg, headless: headless}
}

// Run wires and starts the robot, blocking until ctx is canceled. It tears
// services down in order: scheduler → collector → DB.
func (a *App) Run(ctx context.Context) (err error) {
	clk := clock.Real()

	interval, err := model.ParseCandleInterval(a.cfg.Schedule.Interval)
	if err != nil {
		return fmt.Errorf("app: schedule.interval %q is not a candle interval: %w", a.cfg.Schedule.Interval, err)
	}

	// --- store ---
	db, err := sqlite.Open(ctx, a.cfg.Storage.DBPath)
	if err != nil {
		return fmt.Errorf("app: open store: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("app: close store: %w", cerr)
		}
	}()

	// --- tinvest handshake (fail closed) ---
	client, err := tinvestcli.Resolve(tinvestcli.Config{
		Path:    a.cfg.TInvest.Path,
		Profile: a.cfg.TInvest.Profile,
		Env:     os.Environ(),
	})
	if err != nil {
		return fmt.Errorf("app: resolve tinvest: %w", err)
	}
	info, err := client.Handshake(ctx)
	if err != nil {
		var he *tinvestcli.HandshakeError
		if errors.As(err, &he) {
			return fmt.Errorf("app: tinvest handshake failed, refusing to start: %s", he.Reason)
		}
		return fmt.Errorf("app: tinvest handshake: %w", err)
	}
	log.Printf("app: tinvest %s (contract %s, schema %s) at %s", info.Version, info.Contract, info.SchemaVersion, info.Path)

	// --- portfolio (seed starting cash once) ---
	pf := portfolio.New(clk, baseCurrency)
	if err := pf.Init(ctx, db, parseDec(a.cfg.Paper.StartingCash)); err != nil {
		return fmt.Errorf("app: init portfolio: %w", err)
	}

	// --- paper executor + startup reconciliation ---
	maxQuoteAge := 30 * time.Minute
	sim, err := paper.New(db, clk, portfolioApplier{pf: pf}, a.cfg.Paper, maxQuoteAge, parseDec(a.cfg.Risk.CashFloor), baseCurrency)
	if err != nil {
		return fmt.Errorf("app: build paper executor: %w", err)
	}
	if err := sim.Recover(ctx); err != nil {
		return fmt.Errorf("app: recover order journal: %w", err)
	}

	// --- market collector ---
	store := market.NewSQLiteStore(db)
	collector, err := market.New(market.Deps{
		Broker: market.NewClientBroker(client), Instruments: store, Candles: store,
		Quotes: store, Events: store, Clock: clk,
	}, market.Config{Universe: a.cfg.Universe.Instruments, Interval: interval})
	if err != nil {
		return fmt.Errorf("app: build collector: %w", err)
	}
	// Wire the quote feed into the paper simulator so resting orders fill on the
	// next observation (DESIGN §7). Registered before Start.
	collector.RegisterQuoteListener(func(q model.Quote) {
		if err := sim.OnQuote(ctx, q); err != nil {
			log.Printf("app: on quote: %v", err)
		}
	})
	if err := collector.Start(ctx); err != nil {
		return fmt.Errorf("app: start collector: %w", err)
	}
	defer collector.Stop()

	// --- decision engine + cycle ---
	engine, err := rules.New(rules.DefaultParams(), clk)
	if err != nil {
		return fmt.Errorf("app: build rules engine: %w", err)
	}
	cyc, err := cycle.New(cycle.Deps{DB: db, Clock: clk, Engine: engine, Executor: sim, Portfolio: pf}, cycle.Config{
		Mode:           a.cfg.Mode,
		Interval:       interval,
		Currency:       baseCurrency,
		FeatureParams:  features.DefaultParams(),
		Risk:           a.cfg.Risk,
		Paper:          a.cfg.Paper,
		MaxDataAge:     rules.DefaultParams().MaxDataAge,
		SessionStart:   a.cfg.Schedule.SessionStart,
		SessionEnd:     a.cfg.Schedule.SessionEnd,
		Location:       mustLocation(a.cfg.Schedule.Timezone),
		ConfigSnapshot: configSnapshot(a.cfg),
	})
	if err != nil {
		return fmt.Errorf("app: build cycle engine: %w", err)
	}
	cyc.Start(ctx)
	defer cyc.Stop()

	log.Printf("app: started (mode=%s engine=%s universe=%d interval=%s headless=%v)",
		a.cfg.Mode, a.cfg.Engine.Active, len(a.cfg.Universe.Instruments), interval, a.headless)

	if a.headless {
		a.runHeadless(ctx, cyc, collector)
		return nil
	}
	return a.runTUI(ctx, db, pf, cyc, collector, clk)
}

// runHeadless logs cycle activity until ctx is canceled.
func (a *App) runHeadless(ctx context.Context, cyc *cycle.Engine, collector *market.Collector) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var lastCycle int64
	for {
		select {
		case <-ctx.Done():
			log.Printf("app: shutting down")
			return
		case <-ticker.C:
			st := cyc.Status()
			h := collector.Health()
			if st.LastCycle.ID != lastCycle {
				lastCycle = st.LastCycle.ID
				log.Printf("app: cycle #%d %s (%d decisions, %d orders); stream up=%v",
					st.LastCycle.ID, st.LastCycle.Status, st.LastCycle.Decisions, st.LastCycle.Orders, h.StreamUp)
			}
		}
	}
}

// runTUI builds and runs the Bubble Tea UI bound to the engine and collector.
func (a *App) runTUI(ctx context.Context, db *sqlite.DB, pf *portfolio.Portfolio, cyc *cycle.Engine, collector *market.Collector, clk clock.Clock) error {
	ui, err := tui.New(tui.Deps{
		DB:         db,
		Portfolio:  pf,
		Health:     collector,
		Controller: engineController{e: cyc},
		Canceller:  cancelRequester{e: cyc},
		Clock:      clk,
		Currency:   baseCurrency,
		Mode:       a.cfg.Mode,
	})
	if err != nil {
		return fmt.Errorf("app: build tui: %w", err)
	}
	return ui.Run(ctx)
}

// --- adapters bridging the concrete engine/portfolio to consumer-owned ports ---

// portfolioApplier adapts *portfolio.Portfolio to execution.FillApplier,
// translating between the two packages' identical-shaped FillApplication types.
type portfolioApplier struct{ pf *portfolio.Portfolio }

func (a portfolioApplier) ApplyFill(ctx context.Context, q sqlite.Querier, fa execution.FillApplication) error {
	return a.pf.ApplyFill(ctx, q, portfolio.FillApplication{
		Fill: fa.Fill, InstrumentUID: fa.InstrumentUID, Side: fa.Side, Lot: fa.Lot,
		Currency: fa.Currency, LowFidelity: fa.LowFidelity,
	})
}

// engineController adapts *cycle.Engine to tui.EngineController.
type engineController struct{ e *cycle.Engine }

func (c engineController) Status() tui.EngineStatus {
	s := c.e.Status()
	return tui.EngineStatus{
		State: s.State, Mode: s.Mode, NextCycleAt: s.NextCycleAt,
		LastCycle: tui.CycleSummary{
			ID: s.LastCycle.ID, StartedAt: s.LastCycle.StartedAt, Mode: s.LastCycle.Mode,
			Engine: s.LastCycle.Engine, Status: s.LastCycle.Status,
			Decisions: s.LastCycle.Decisions, Orders: s.LastCycle.Orders,
		},
	}
}

func (c engineController) Pause()      { c.e.Pause() }
func (c engineController) Resume()     { c.e.Resume() }
func (c engineController) KillSwitch() { c.e.KillSwitch() }

// cancelRequester adapts *cycle.Engine to tui.CancelRequester.
type cancelRequester struct{ e *cycle.Engine }

func (c cancelRequester) RequestCancel(clientOrderID string) error {
	return c.e.RequestCancel(clientOrderID)
}

func parseDec(s string) model.Decimal {
	d, err := model.ParseDecimal(s)
	if err != nil {
		return model.Decimal{}
	}
	return d
}

func mustLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

func configSnapshot(cfg *config.Config) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return string(b)
}
