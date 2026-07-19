// Package config loads and validates the robot.toml configuration file.
// Decoding is strict: any key not recognized by the struct tags below is a
// load error, not a silently ignored typo.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Config is the root of robot.toml.
type Config struct {
	Mode string `toml:"mode"`

	TInvest  TInvestConfig  `toml:"tinvest"`
	Universe UniverseConfig `toml:"universe"`
	Schedule ScheduleConfig `toml:"schedule"`
	Engine   EngineConfig   `toml:"engine"`
	Risk     RiskConfig     `toml:"risk"`
	Paper    PaperConfig    `toml:"paper"`
	Storage  StorageConfig  `toml:"storage"`
	Real     RealConfig     `toml:"real"`
}

// TInvestConfig configures how the tinvest CLI is located and invoked.
type TInvestConfig struct {
	// Path overrides binary resolution; empty means resolve via $PATH.
	Path    string `toml:"path"`
	Profile string `toml:"profile"`
	Account string `toml:"account"`
}

// UniverseConfig lists the instruments the robot trades.
type UniverseConfig struct {
	Instruments []string `toml:"instruments"`
}

// ScheduleConfig controls decision-cycle cadence and trading session hours.
type ScheduleConfig struct {
	// Interval is a Go duration string, e.g. "5m".
	Interval string `toml:"interval"`
	// SessionStart/SessionEnd are "HH:MM" in Timezone; empty means no
	// session restriction (24h).
	SessionStart string `toml:"session_start"`
	SessionEnd   string `toml:"session_end"`
	Timezone     string `toml:"timezone"`
}

// EngineConfig selects the decision engine.
type EngineConfig struct {
	Active string `toml:"active"`
}

// RiskConfig holds pre-trade safety limits. Money fields are decimal
// strings, matching the tinvest contract.
type RiskConfig struct {
	MaxPositionNotional string   `toml:"max_position_notional"`
	MaxTotalExposure    string   `toml:"max_total_exposure"`
	MaxOrdersPerCycle   int      `toml:"max_orders_per_cycle"`
	MaxOrdersPerDay     int      `toml:"max_orders_per_day"`
	MaxDailyLoss        string   `toml:"max_daily_loss"`
	Allowlist           []string `toml:"allowlist"`
	CashFloor           string   `toml:"cash_floor"`
}

// PaperConfig controls the paper-trading fill simulator.
type PaperConfig struct {
	StartingCash   string `toml:"starting_cash"`
	SlippageBps    int    `toml:"slippage_bps"`
	CommissionRate string `toml:"commission_rate"`
}

// StorageConfig points at the SQLite database file.
type StorageConfig struct {
	DBPath string `toml:"db_path"`
}

// RealConfig gates real (live) trading. Phase 1 requires Enable to stay
// false; the app refuses to start otherwise.
type RealConfig struct {
	Enable bool `toml:"enable"`
}

// sessionTimeLayout is the strict wall-clock format for schedule.session_start
// and schedule.session_end.
const sessionTimeLayout = "15:04"

// DefaultDir returns the invest-robot configuration directory, honoring
// XDG_CONFIG_HOME: $XDG_CONFIG_HOME/invest-robot when that variable is set,
// otherwise $HOME/.config/invest-robot. This applies on all platforms,
// including macOS, so the robot shares the ~/.config layout with the sibling
// tinvest CLI rather than landing under ~/Library/Application Support.
// Resolution is fail-closed: without an XDG directory or a resolvable home
// directory the caller must set paths explicitly.
func DefaultDir() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: resolve config home directory: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "invest-robot"), nil
}

// Load reads, strictly decodes, defaults, and validates the config file at
// path.
func Load(path string) (*Config, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("config: unknown key(s) in %s: %s", path, strings.Join(keys, ", "))
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Mode == "" {
		c.Mode = "paper"
	}
	if c.Schedule.Interval == "" {
		c.Schedule.Interval = "5m"
	}
	if c.Schedule.Timezone == "" {
		c.Schedule.Timezone = "Europe/Moscow"
	}
	if c.Engine.Active == "" {
		c.Engine.Active = "rules"
	}
	// Risk order-count limits are safety-critical and get no silent
	// default: a TOML zero value is indistinguishable from "not set", so
	// defaulting here would mask an operator's explicit 0.
	if c.Paper.StartingCash == "" {
		c.Paper.StartingCash = "100000"
	}
	if c.Paper.CommissionRate == "" {
		c.Paper.CommissionRate = "0"
	}
	if c.Storage.DBPath == "" {
		if dir, err := DefaultDir(); err == nil {
			c.Storage.DBPath = filepath.Join(dir, "robot.db")
		}
	}
}

// Validate checks defaulted config for consistency and safety. Phase 1
// hard-refuses any path toward real trading.
func (c *Config) Validate() error {
	switch c.Mode {
	case "paper", "backtest", "real":
	default:
		return fmt.Errorf("config: mode must be one of paper, backtest, real (got %q)", c.Mode)
	}
	if c.Real.Enable || c.Mode == "real" {
		return fmt.Errorf("config: real trading is not available in phase 1; real.enable must be false and mode must not be \"real\"")
	}

	interval, err := time.ParseDuration(c.Schedule.Interval)
	if err != nil {
		return fmt.Errorf("config: schedule.interval invalid: %w", err)
	}
	if interval <= 0 {
		// A zero or negative cadence would panic time.NewTicker at startup.
		return fmt.Errorf("config: schedule.interval must be positive (got %q)", c.Schedule.Interval)
	}
	if c.Schedule.Timezone != "" {
		if _, err := time.LoadLocation(c.Schedule.Timezone); err != nil {
			return fmt.Errorf("config: schedule.timezone invalid: %w", err)
		}
	}
	if err := validateSession(c.Schedule.SessionStart, c.Schedule.SessionEnd); err != nil {
		return err
	}

	// Phase 1 ships only the deterministic rules engine; "claude-cli" and
	// "anthropic-api" arrive in Phase 2 (DESIGN §8, §14). Allowing an arbitrary
	// name here would defer a typo to a late "unknown engine" failure.
	if c.Engine.Active != "rules" {
		return fmt.Errorf("config: engine.active must be \"rules\" in phase 1 (got %q)", c.Engine.Active)
	}

	if err := validateDecimal("risk.max_position_notional", c.Risk.MaxPositionNotional); err != nil {
		return err
	}
	if err := validateDecimal("risk.max_total_exposure", c.Risk.MaxTotalExposure); err != nil {
		return err
	}
	if err := validateDecimal("risk.max_daily_loss", c.Risk.MaxDailyLoss); err != nil {
		return err
	}
	if err := validateDecimal("risk.cash_floor", c.Risk.CashFloor); err != nil {
		return err
	}
	if c.Risk.MaxOrdersPerCycle <= 0 {
		return fmt.Errorf("config: risk.max_orders_per_cycle must be positive")
	}
	if c.Risk.MaxOrdersPerDay <= 0 {
		return fmt.Errorf("config: risk.max_orders_per_day must be positive")
	}

	if err := validateDecimal("paper.starting_cash", c.Paper.StartingCash); err != nil {
		return err
	}
	if err := validateDecimal("paper.commission_rate", c.Paper.CommissionRate); err != nil {
		return err
	}
	if c.Paper.SlippageBps < 0 {
		return fmt.Errorf("config: paper.slippage_bps must not be negative")
	}

	if c.Storage.DBPath == "" {
		return fmt.Errorf("config: storage.db_path could not be defaulted (config home directory unavailable); set it explicitly")
	}

	return nil
}

// validateDecimal enforces that a money field parses as a model.Decimal and is
// non-negative. Parsing through model.ParseDecimal (not just a regex) rejects
// the values a regex would wave through but the fixed-point type cannot hold:
// more than nine fractional digits, and magnitudes outside the int64-nano
// range. Catching those here means a safety limit that looks fine in the file
// cannot fail later when it is converted to a Decimal.
func validateDecimal(field, value string) error {
	if value == "" {
		return fmt.Errorf("config: %s must not be empty", field)
	}
	d, err := model.ParseDecimal(value)
	if err != nil {
		return fmt.Errorf("config: %s invalid: %w", field, err)
	}
	if d.Sign() < 0 {
		return fmt.Errorf("config: %s must not be negative (got %q)", field, value)
	}
	return nil
}

// validateSession enforces that the trading-session window is either fully
// absent (24h trading) or fully specified as a valid start < end pair in
// "HH:MM". A half-specified or malformed window is rejected at startup rather
// than silently ignored.
func validateSession(start, end string) error {
	if (start == "") != (end == "") {
		return fmt.Errorf("config: schedule.session_start and schedule.session_end must be set together or both left empty")
	}
	if start == "" {
		return nil
	}
	st, err := time.Parse(sessionTimeLayout, start)
	if err != nil {
		return fmt.Errorf("config: schedule.session_start must be HH:MM (got %q)", start)
	}
	et, err := time.Parse(sessionTimeLayout, end)
	if err != nil {
		return fmt.Errorf("config: schedule.session_end must be HH:MM (got %q)", end)
	}
	if !st.Before(et) {
		return fmt.Errorf("config: schedule.session_start (%s) must be before schedule.session_end (%s)", start, end)
	}
	return nil
}
