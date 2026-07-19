package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "robot.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

const minimalValidConfig = `
[universe]
instruments = ["SBER"]

[risk]
max_position_notional = "50000"
max_total_exposure = "150000"
max_orders_per_cycle = 3
max_orders_per_day = 10
max_daily_loss = "5000"
cash_floor = "10000"
`

func TestLoad_ExampleConfig(t *testing.T) {
	cfg, err := Load("../../robot.example.toml")
	if err != nil {
		t.Fatalf("Load(robot.example.toml) failed: %v", err)
	}
	if cfg.Mode != "paper" {
		t.Errorf("Mode = %q, want paper", cfg.Mode)
	}
	if len(cfg.Universe.Instruments) != 2 {
		t.Errorf("Universe.Instruments = %v, want 2 entries", cfg.Universe.Instruments)
	}
	if cfg.Engine.Active != "rules" {
		t.Errorf("Engine.Active = %q, want rules", cfg.Engine.Active)
	}
	if cfg.Real.Enable {
		t.Errorf("Real.Enable = true, want false")
	}
}

func TestLoad_UnknownKeyRejected(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\nbogus_top_level = true\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with unknown key succeeded, want error")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error = %v, want it to mention unknown key", err)
	}
}

func TestLoad_UnknownNestedKeyRejected(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[paper]\nstarting_cash = \"100000\"\nbogus = 1\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with unknown nested key succeeded, want error")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error = %v, want it to mention unknown key", err)
	}
}

func TestLoad_Defaults(t *testing.T) {
	path := writeTemp(t, minimalValidConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Mode != "paper" {
		t.Errorf("Mode = %q, want paper", cfg.Mode)
	}
	if cfg.Schedule.Interval != "5m" {
		t.Errorf("Schedule.Interval = %q, want 5m", cfg.Schedule.Interval)
	}
	if cfg.Schedule.Timezone != "Europe/Moscow" {
		t.Errorf("Schedule.Timezone = %q, want Europe/Moscow", cfg.Schedule.Timezone)
	}
	if cfg.Engine.Active != "rules" {
		t.Errorf("Engine.Active = %q, want rules", cfg.Engine.Active)
	}
	if cfg.Paper.StartingCash != "100000" {
		t.Errorf("Paper.StartingCash = %q, want 100000", cfg.Paper.StartingCash)
	}
	if cfg.Paper.CommissionRate != "0" {
		t.Errorf("Paper.CommissionRate = %q, want 0", cfg.Paper.CommissionRate)
	}
	if cfg.Storage.DBPath == "" {
		t.Error("Storage.DBPath defaulted to empty, want a UserConfigDir-based path")
	}
	if !strings.Contains(cfg.Storage.DBPath, filepath.Join("invest-robot", "robot.db")) {
		t.Errorf("Storage.DBPath = %q, want it to contain invest-robot/robot.db", cfg.Storage.DBPath)
	}
}

func TestValidate_RealEnableRejected(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[real]\nenable = true\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with real.enable = true succeeded, want error")
	}
	if !strings.Contains(err.Error(), "phase 1") {
		t.Errorf("error = %v, want it to mention phase 1", err)
	}
}

func TestValidate_RealModeRejected(t *testing.T) {
	path := writeTemp(t, "mode = \"real\"\n"+minimalValidConfig)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with mode = real succeeded, want error")
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	path := writeTemp(t, "mode = \"bogus\"\n"+minimalValidConfig)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with invalid mode succeeded, want error")
	}
}

func TestValidate_InvalidScheduleInterval(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[schedule]\ninterval = \"not-a-duration\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with invalid schedule.interval succeeded, want error")
	}
}

func TestValidate_InvalidTimezone(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[schedule]\ntimezone = \"Not/A_Zone\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with invalid schedule.timezone succeeded, want error")
	}
}

func TestValidate_InvalidDecimalField(t *testing.T) {
	path := writeTemp(t, strings.Replace(minimalValidConfig, `max_position_notional = "50000"`, `max_position_notional = "not-a-number"`, 1))
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with non-decimal risk field succeeded, want error")
	}
	if !strings.Contains(err.Error(), "max_position_notional") {
		t.Errorf("error = %v, want it to mention the offending field", err)
	}
}

func TestValidate_DecimalTooPrecise(t *testing.T) {
	// Ten fractional digits: passes a naive regex but exceeds model.Decimal's
	// nine-digit scale, so it must be rejected at load time, not later.
	path := writeTemp(t, strings.Replace(minimalValidConfig, `max_position_notional = "50000"`, `max_position_notional = "1.0000000001"`, 1))
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with a 10-fractional-digit limit succeeded, want error")
	}
	if !strings.Contains(err.Error(), "max_position_notional") {
		t.Errorf("error = %v, want it to mention the offending field", err)
	}
}

func TestValidate_DecimalOverflow(t *testing.T) {
	// Above model.Decimal's representable range (~9.22e9).
	path := writeTemp(t, strings.Replace(minimalValidConfig, `max_total_exposure = "150000"`, `max_total_exposure = "10000000000"`, 1))
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with an out-of-range limit succeeded, want error")
	}
	if !strings.Contains(err.Error(), "max_total_exposure") {
		t.Errorf("error = %v, want it to mention the offending field", err)
	}
}

func TestValidate_ZeroInterval(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[schedule]\ninterval = \"0s\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with a zero schedule.interval succeeded, want error")
	}
	if !strings.Contains(err.Error(), "interval") {
		t.Errorf("error = %v, want it to mention the interval", err)
	}
}

func TestValidate_UnknownEngine(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[engine]\nactive = \"claude-cli\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with a non-Phase-1 engine succeeded, want error")
	}
	if !strings.Contains(err.Error(), "engine.active") {
		t.Errorf("error = %v, want it to mention engine.active", err)
	}
}

func TestValidate_SessionHalfSpecified(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[schedule]\nsession_start = \"10:00\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with only session_start succeeded, want error")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Errorf("error = %v, want it to mention the session window", err)
	}
}

func TestValidate_SessionStartNotBeforeEnd(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[schedule]\nsession_start = \"18:00\"\nsession_end = \"10:00\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with session_start after session_end succeeded, want error")
	}
}

func TestValidate_SessionMalformed(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[schedule]\nsession_start = \"25:00\"\nsession_end = \"26:00\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with a malformed session time succeeded, want error")
	}
}

func TestValidate_SessionWellFormed(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[schedule]\nsession_start = \"10:00\"\nsession_end = \"18:45\"\n")
	if _, err := Load(path); err != nil {
		t.Fatalf("Load() with a valid session window failed: %v", err)
	}
}

func TestValidate_MissingRequiredRiskField(t *testing.T) {
	path := writeTemp(t, `
[universe]
instruments = ["SBER"]

[risk]
max_total_exposure = "150000"
max_orders_per_cycle = 3
max_orders_per_day = 10
max_daily_loss = "5000"
cash_floor = "10000"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with missing risk.max_position_notional succeeded, want error")
	}
}

func TestValidate_MissingOrderLimits(t *testing.T) {
	// max_orders_per_cycle/day get no default (see applyDefaults):
	// omitting them must fail validation, not silently apply a default.
	path := writeTemp(t, `
[universe]
instruments = ["SBER"]

[risk]
max_position_notional = "50000"
max_total_exposure = "150000"
max_daily_loss = "5000"
cash_floor = "10000"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with missing order limits succeeded, want error")
	}
}

func TestValidate_NegativeSlippage(t *testing.T) {
	path := writeTemp(t, minimalValidConfig+"\n[paper]\nslippage_bps = -1\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with negative slippage_bps succeeded, want error")
	}
}

func TestValidate_NonPositiveOrderLimits(t *testing.T) {
	path := writeTemp(t, `
[universe]
instruments = ["SBER"]

[risk]
max_orders_per_cycle = 0
max_position_notional = "1"
max_total_exposure = "1"
max_daily_loss = "1"
cash_floor = "1"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() with max_orders_per_cycle = 0 succeeded, want error")
	}
}

func TestDefaultDir_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/here")
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir() failed: %v", err)
	}
	want := filepath.Join("/xdg/here", "invest-robot")
	if dir != want {
		t.Errorf("DefaultDir() = %q, want %q", dir, want)
	}
}

func TestDefaultDir_HomeFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/andrew")
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir() failed: %v", err)
	}
	want := filepath.Join("/home/andrew", ".config", "invest-robot")
	if dir != want {
		t.Errorf("DefaultDir() = %q, want %q", dir, want)
	}
}

func TestApplyDefaults_DBPathUnderXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/here")
	path := writeTemp(t, minimalValidConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	want := filepath.Join("/xdg/here", "invest-robot", "robot.db")
	if cfg.Storage.DBPath != want {
		t.Errorf("Storage.DBPath = %q, want %q", cfg.Storage.DBPath, want)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err == nil {
		t.Fatal("Load() of missing file succeeded, want error")
	}
}
