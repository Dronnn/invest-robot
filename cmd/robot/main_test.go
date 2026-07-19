package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRun_Version(t *testing.T) {
	if code := run([]string{"--version"}); code != 0 {
		t.Errorf("run(--version) = %d, want 0", code)
	}
}

func TestRun_MissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.toml")
	if code := run([]string{"--config", path}); code != 1 {
		t.Errorf("run(--config missing) = %d, want 1", code)
	}
}

func TestRun_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "robot.toml")
	contents := `
[universe]
instruments = ["SBER"]

[risk]
max_position_notional = "50000"
max_total_exposure = "150000"
max_orders_per_cycle = 5
max_orders_per_day = 20
max_daily_loss = "5000"
cash_floor = "10000"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if code := run([]string{"--config", path}); code != 0 {
		t.Errorf("run(--config valid) = %d, want 0", code)
	}
}

func TestRun_BadFlag(t *testing.T) {
	if code := run([]string{"--not-a-flag"}); code != 2 {
		t.Errorf("run(--not-a-flag) = %d, want 2", code)
	}
}

func TestDefaultConfigPath_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/here")
	got := defaultConfigPath()
	want := filepath.Join("/xdg/here", "invest-robot", "robot.toml")
	if got != want {
		t.Errorf("defaultConfigPath() = %q, want %q", got, want)
	}
}
