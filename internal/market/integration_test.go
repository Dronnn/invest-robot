package market_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/market"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
	"github.com/Dronnn/invest-robot/internal/tinvestcli"
)

var fakeBin string

func TestMain(m *testing.M) {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate repo root:", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "faketinvest-market")
	if err != nil {
		fmt.Fprintln(os.Stderr, "temp dir:", err)
		os.Exit(1)
	}
	fakeBin = filepath.Join(dir, "faketinvest")
	build := exec.Command("go", "build", "-o", fakeBin, "./test/faketinvest")
	build.Dir = root
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build faketinvest:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

// TestCollectorAgainstFakeTinvest exercises the real tinvest client + faketinvest
// through the collector: startup backfill from the unary candles fixture and
// quote ingest from the marketdata stream.
func TestCollectorAgainstFakeTinvest(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	scenario := filepath.Join(root, "test", "faketinvest", "scenarios", "happy")

	client, err := tinvestcli.Resolve(tinvestcli.Config{
		Path: fakeBin,
		Env:  append(os.Environ(), "FAKETINVEST_SCENARIO="+scenario, "FAKETINVEST_STATE="+t.TempDir()),
		// Keep the (finite) happy stream from tripping the circuit during the test.
		StreamBaseBackoff:     20 * time.Millisecond,
		StreamMinHealthyRun:   time.Nanosecond,
		StreamMaxFastRestarts: 1000,
	})
	if err != nil {
		t.Fatalf("resolve client: %v", err)
	}

	db, s := tempStore(t)
	c, err := market.New(deps(market.NewClientBroker(client), s, clock.Real()), market.Config{
		Universe: []string{"SBER@TQBR", "GAZP@TQBR"}, Interval: model.Interval5m,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	ctx := context.Background()

	// Startup backfill writes the SBER unary candle fixture (four bars, the last
	// incomplete).
	waitFor(t, "backfilled SBER candles", func() bool {
		got, err := sqlite.CandleRepo{}.Range(ctx, db, uidSBER, model.Interval5m,
			time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC), time.Date(2026, 7, 19, 9, 20, 0, 0, time.UTC))
		return err == nil && len(got) == 4
	})
	wm, ok, err := sqlite.CandleRepo{}.LatestComplete(ctx, db, uidSBER, model.Interval5m)
	if err != nil || !ok {
		t.Fatalf("watermark: ok=%v err=%v", ok, err)
	}
	if !wm.Complete {
		t.Fatal("watermark bar should be complete")
	}

	// The marketdata stream delivers SBER last prices, ingested as quotes.
	waitFor(t, "SBER quote from stream", func() bool {
		q, ok, err := sqlite.QuoteRepo{}.Latest(ctx, db, uidSBER)
		return err == nil && ok && !q.Last.IsZero()
	})

	// Health reflects a live stream and the SBER watermark.
	h := c.Health()
	if ih, ok := h.Instruments[uidSBER]; !ok || ih.Ticker != "SBER" {
		t.Fatalf("SBER not in health: %+v", h.Instruments)
	}
}
