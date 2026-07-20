package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Dronnn/invest-robot/internal/config"
)

// TestRun_FailsClosedWithoutTinvest verifies the app refuses to start when the
// tinvest binary cannot be resolved (DESIGN §4), rather than proceeding. It
// wires against a bogus tinvest path so the outcome is deterministic whether or
// not tinvest is installed.
func TestRun_FailsClosedWithoutTinvest(t *testing.T) {
	cfg := &config.Config{
		Mode:     "paper",
		Engine:   config.EngineConfig{Active: "rules"},
		Schedule: config.ScheduleConfig{Interval: "5m", Timezone: "UTC"},
		Storage:  config.StorageConfig{DBPath: filepath.Join(t.TempDir(), "robot.db")},
		TInvest:  config.TInvestConfig{Path: filepath.Join(t.TempDir(), "no-such-tinvest")},
		Paper:    config.PaperConfig{StartingCash: "100000", CommissionRate: "0"},
		Universe: config.UniverseConfig{Instruments: []string{"SBER@TQBR"}},
	}
	err := New(cfg, true).Run(context.Background())
	if err == nil {
		t.Fatal("Run() should fail closed without a resolvable tinvest binary")
	}
}
