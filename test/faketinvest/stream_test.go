package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// decodeFrames splits NDJSON output into decoded stream frames, asserting each
// line is one independent JSON object carrying the pinned schema_version.
func decodeFrames(t *testing.T, out string) []streamFrame {
	t.Helper()
	var frames []streamFrame
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var f streamFrame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("frame is not valid JSON: %q: %v", line, err)
		}
		if f.SchemaVersion != defaultSchemaVersion {
			t.Errorf("frame %q schema_version = %q, want %q", f.Type, f.SchemaVersion, defaultSchemaVersion)
		}
		if f.Time == "" {
			t.Errorf("frame %q has empty time", f.Type)
		}
		frames = append(frames, f)
	}
	return frames
}

func TestStreamHappyPlayback(t *testing.T) {
	code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": happyDir},
		"stream", "marketdata", "--instrument", "SBER@TQBR", "--instrument", "GAZP@TQBR", "--candles=5m", "-o", "json")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	frames := decodeFrames(t, out)
	if len(frames) != 5 {
		t.Fatalf("frame count = %d, want 5", len(frames))
	}
	if frames[0].Type != "connected" {
		t.Errorf("first frame type = %q, want connected", frames[0].Type)
	}
	types := map[string]int{}
	for _, f := range frames {
		types[f.Type]++
	}
	if types["candle"] != 2 || types["last_price"] != 2 {
		t.Errorf("expected 2 candles + 2 last_price, got %v", types)
	}
}

// TestStreamDisconnectGap verifies the hostile stream reproduces an in-band
// disconnect, a candle time gap after reconnect, and a process exit (so the
// robot's supervisor would restart the child).
func TestStreamDisconnectGap(t *testing.T) {
	code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": hostileDir},
		"stream", "marketdata", "--instrument", "SBER@TQBR", "--candles=5m", "-o", "json")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	frames := decodeFrames(t, out)

	var sawDisconnect, sawReconnect bool
	var candleTimes []string
	for _, f := range frames {
		switch f.Type {
		case "disconnected":
			sawDisconnect = true
		case "connected":
			if sawDisconnect {
				sawReconnect = true
			}
		case "candle":
			var c struct {
				CandleTime string `json:"candle_time"`
			}
			_ = json.Unmarshal(f.Data, &c)
			candleTimes = append(candleTimes, c.CandleTime)
		}
	}
	if !sawDisconnect {
		t.Error("no disconnected frame")
	}
	if !sawReconnect {
		t.Error("no reconnect (connected after disconnected)")
	}
	// The script emits 09:00 and 09:05, then skips 09:10 and resumes at 09:15.
	want := []string{"2026-07-19T09:00:00Z", "2026-07-19T09:05:00Z", "2026-07-19T09:15:00Z"}
	if strings.Join(candleTimes, ",") != strings.Join(want, ",") {
		t.Errorf("candle times = %v, want %v (gap at 09:10)", candleTimes, want)
	}
}

// TestStreamGracefulShutdown drives the context-cancellation shutdown path: a
// cancelled context makes the stream emit a final disconnected/shutdown frame
// and exit 0, matching the real CLI's clean shutdown.
func TestStreamGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scenario.toml"), `
account_id = "acc"
[stream]
script = "s.json"
shutdown_time = "2026-07-19T10:05:00Z"
`)
	// The second frame blocks for 10s so cancellation is always caught mid-stream.
	writeFile(t, filepath.Join(dir, "s.json"), `[
  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
  {"type":"candle","time":"2026-07-19T10:00:00Z","delay_ms":10000,"data":{"candle_time":"2026-07-19T10:00:00Z"}}
]`)

	ctx, cancel := context.WithCancel(context.Background())
	env := func(k string) string {
		if k == "FAKETINVEST_SCENARIO" {
			return dir
		}
		return ""
	}
	var stdout bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"stream", "marketdata", "-o", "json"}, env, &stdout, &bytes.Buffer{})
	}()
	cancel()

	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit = %d, want 0 on graceful shutdown", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not shut down within 5s of cancel")
	}

	frames := decodeFrames(t, stdout.String())
	last := frames[len(frames)-1]
	if last.Type != "disconnected" {
		t.Fatalf("last frame type = %q, want disconnected", last.Type)
	}
	var d struct {
		Reason string `json:"reason"`
		Final  bool   `json:"final"`
	}
	if err := json.Unmarshal(last.Data, &d); err != nil {
		t.Fatalf("decode shutdown data: %v", err)
	}
	if d.Reason != "shutdown" || !d.Final {
		t.Errorf("shutdown frame = %+v, want reason=shutdown final=true", d)
	}
}

// TestBinarySignalShutdown builds the fake and drives its real SIGTERM handler
// end to end, covering main.go's signal wiring and the built-binary invocation
// path integration tests use.
func TestBinarySignalShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM semantics differ on Windows")
	}
	bin := buildFake(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scenario.toml"), `
account_id = "acc"
[stream]
script = "s.json"
shutdown_time = "2026-07-19T10:05:00Z"
`)
	writeFile(t, filepath.Join(dir, "s.json"), `[
  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
  {"type":"candle","time":"2026-07-19T10:00:00Z","delay_ms":30000,"data":{"candle_time":"2026-07-19T10:00:00Z"}}
]`)

	cmd := exec.Command(bin, "stream", "marketdata", "-o", "json")
	cmd.Env = append(os.Environ(), "FAKETINVEST_SCENARIO="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Reading the first frame confirms the process is up before we signal it —
	// no arbitrary sleep needed.
	reader := bufio.NewReader(stdout)
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if !strings.Contains(firstLine, `"type":"connected"`) {
		t.Fatalf("first frame = %q, want connected", firstLine)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	rest, _ := reader.ReadString('\n')
	if !strings.Contains(rest, `"type":"disconnected"`) || !strings.Contains(rest, `"reason":"shutdown"`) {
		t.Fatalf("post-signal frame = %q, want disconnected shutdown", rest)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("process exited non-zero after SIGTERM: %v", err)
	}
}

// buildFake compiles the fake into a temp dir and returns the binary path.
func buildFake(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "faketinvest")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return bin
}
