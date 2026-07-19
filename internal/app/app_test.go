package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/config"
)

func TestRun_StopsOnContextCancel(t *testing.T) {
	a := New(&config.Config{Mode: "paper", Engine: config.EngineConfig{Active: "rules"}})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := a.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run() error = %v, want context.DeadlineExceeded", err)
	}
}
