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
		"stream", "marketdata", "--instrument", "SBER@TQBR", "--instrument", "GAZP@TQBR", "--candles=5m", "--last-price", "-o", "json")
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

// TestStreamSubscriptionFiltering proves the fake actually filters replayed
// frames instead of dumping the whole script: the happy script carries candle
// and last_price frames for both SBER and GAZP, but a request that subscribes
// only to SBER candles must see none of GAZP's frames and none of the
// last_price frames (last-price was never requested).
func TestStreamSubscriptionFiltering(t *testing.T) {
	code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": happyDir},
		"stream", "marketdata", "--instrument", "SBER@TQBR", "--candles=5m", "-o", "json")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	frames := decodeFrames(t, out)
	var candles, others int
	for _, f := range frames {
		switch f.Type {
		case "candle":
			candles++
			var peek struct {
				InstrumentUID string `json:"instrument_uid"`
			}
			_ = json.Unmarshal(f.Data, &peek)
			if peek.InstrumentUID != "e6123145-9665-43e0-8413-cd61b8aa9b13" {
				t.Errorf("candle for unsubscribed instrument %q leaked through", peek.InstrumentUID)
			}
		case "last_price":
			t.Errorf("unsubscribed last_price frame leaked through: %+v", f)
		case "connected":
		default:
			others++
		}
	}
	if candles != 1 {
		t.Errorf("candle count = %d, want 1 (SBER only)", candles)
	}

	// Requesting an interval the script never emits must filter every candle out.
	code, out = invoke(t, map[string]string{"FAKETINVEST_SCENARIO": happyDir},
		"stream", "marketdata", "--instrument", "SBER@TQBR", "--candles=1m", "-o", "json")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, f := range decodeFrames(t, out) {
		if f.Type == "candle" {
			t.Errorf("candle frame at the wrong interval was not filtered out: %+v", f)
		}
	}
}

// TestStreamOrderbookReplay proves the fake replays order-book frames when the
// request subscribes to --orderbook, and filters them out otherwise.
func TestStreamOrderbookReplay(t *testing.T) {
	uid := "e6123145-9665-43e0-8413-cd61b8aa9b13"
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scenario.toml"), `
account_id = "acc"
[[instruments]]
uid = "`+uid+`"
ticker = "SBER"
class_code = "TQBR"
[stream]
script = "s.json"
`)
	writeFile(t, filepath.Join(dir, "s.json"), `[
  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
  {"type":"orderbook","time":"2026-07-19T10:00:00Z","data":{"instrument_uid":"`+uid+`","depth":10,"bids":[{"price":{"value":"270.4"},"quantity":"120"}],"asks":[{"price":{"value":"270.6"},"quantity":"90"}],"orderbook_time":"2026-07-19T10:00:00Z"}}
]`)

	// Subscribed: the order-book frame is replayed with its top-of-book.
	code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": dir},
		"stream", "marketdata", "--instrument", uid, "--orderbook=10", "-o", "json")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0; out:\n%s", code, out)
	}
	var ob *streamFrame
	for _, f := range decodeFrames(t, out) {
		if f.Type == "orderbook" {
			frame := f
			ob = &frame
			break
		}
	}
	if ob == nil {
		t.Fatal("no orderbook frame replayed under an --orderbook subscription")
	}
	var data struct {
		Bids []struct {
			Price struct {
				Value string `json:"value"`
			} `json:"price"`
		} `json:"bids"`
		Asks []struct {
			Price struct {
				Value string `json:"value"`
			} `json:"price"`
		} `json:"asks"`
	}
	if err := json.Unmarshal(ob.Data, &data); err != nil {
		t.Fatalf("decode orderbook data: %v", err)
	}
	if len(data.Bids) == 0 || data.Bids[0].Price.Value != "270.4" {
		t.Errorf("best bid = %+v, want 270.4", data.Bids)
	}
	if len(data.Asks) == 0 || data.Asks[0].Price.Value != "270.6" {
		t.Errorf("best ask = %+v, want 270.6", data.Asks)
	}

	// Not subscribed (candles only): the order-book frame is filtered out.
	code, out = invoke(t, map[string]string{"FAKETINVEST_SCENARIO": dir},
		"stream", "marketdata", "--instrument", uid, "--candles=5m", "-o", "json")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, f := range decodeFrames(t, out) {
		if f.Type == "orderbook" {
			t.Error("orderbook frame leaked through without an --orderbook subscription")
		}
	}
}

// TestStreamSubscriptionValidation mirrors the real CLI's local (no-network)
// `stream marketdata` validation: at least one --instrument, at least one
// data-kind flag, a recognized --candles interval, and a recognized
// --orderbook depth. Every failure is USAGE, exit 2, delivered as a stream
// error frame (never a bare unary envelope — the stream contract is NDJSON).
func TestStreamSubscriptionValidation(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"no_instrument", []string{"stream", "marketdata", "--candles=5m", "-o", "json"}},
		{"no_data_kind", []string{"stream", "marketdata", "--instrument", "SBER@TQBR", "-o", "json"}},
		{"invalid_interval", []string{"stream", "marketdata", "--instrument", "SBER@TQBR", "--candles=7m", "-o", "json"}},
		{"invalid_depth", []string{"stream", "marketdata", "--instrument", "SBER@TQBR", "--orderbook=7", "-o", "json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": happyDir}, tc.argv...)
			if code != exitUsage {
				t.Fatalf("exit = %d, want %d; out:\n%s", code, exitUsage, out)
			}
			frames := decodeFrames(t, out)
			if len(frames) != 1 || frames[0].Type != "error" {
				t.Fatalf("frames = %+v, want a single error frame", frames)
			}
			if frames[0].Error == nil || frames[0].Error.Code != "USAGE" {
				t.Fatalf("error = %+v, want code USAGE", frames[0].Error)
			}
		})
	}
}

// TestStreamUnknownInstrumentRejected proves an alias (ticker@classcode) that
// isn't in the scenario's instrument universe is BROKER_REJECTED (exit 5),
// mirroring the real CLI's resolveAll -> classifyResolveErr path (the same
// classification the fake already gives an unknown instrument on every unary
// command) — grounded in reading cmd/tinvest/instruments.go's
// classifyResolveErr and internal/render/errors.go's NotFound mapping in the
// sibling tinvest repo, not merely assumed; see README.md.
func TestStreamUnknownInstrumentRejected(t *testing.T) {
	code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": happyDir},
		"stream", "marketdata", "--instrument", "NOPE@TQBR", "--candles=5m", "-o", "json")
	if code != exitRejected {
		t.Fatalf("exit = %d, want %d; out:\n%s", code, exitRejected, out)
	}
	frames := decodeFrames(t, out)
	if len(frames) != 1 || frames[0].Type != "error" {
		t.Fatalf("frames = %+v, want a single error frame", frames)
	}
	if frames[0].Error == nil || frames[0].Error.Code != "BROKER_REJECTED" {
		t.Fatalf("error = %+v, want code BROKER_REJECTED", frames[0].Error)
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
[[instruments]]
uid = "e6123145-9665-43e0-8413-cd61b8aa9b13"
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
		done <- run(ctx, []string{"stream", "marketdata", "--instrument", "e6123145-9665-43e0-8413-cd61b8aa9b13", "--candles=5m", "-o", "json"}, env, &stdout, &bytes.Buffer{})
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
[[instruments]]
uid = "e6123145-9665-43e0-8413-cd61b8aa9b13"
[stream]
script = "s.json"
shutdown_time = "2026-07-19T10:05:00Z"
`)
	writeFile(t, filepath.Join(dir, "s.json"), `[
  {"type":"connected","time":"2026-07-19T10:00:00Z","data":{"attempt":1,"subscriptions":1}},
  {"type":"candle","time":"2026-07-19T10:00:00Z","delay_ms":30000,"data":{"candle_time":"2026-07-19T10:00:00Z"}}
]`)

	cmd := exec.Command(bin, "stream", "marketdata", "--instrument", "e6123145-9665-43e0-8413-cd61b8aa9b13", "--candles=5m", "-o", "json")
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
