package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// defaultStreamTime is used for generated frames (a signal-driven shutdown)
// when the scenario pins no explicit timestamp, keeping output deterministic.
const defaultStreamTime = "2026-07-19T10:00:00Z"

// scriptEntry is one line of a stream script. A normal frame sets Type (and
// usually Data); an entry that sets Exit terminates the process with that code
// after emitting its frame, simulating a mid-stream process death for the
// robot's stream supervisor to restart.
type scriptEntry struct {
	Type      string          `json:"type"`
	Time      string          `json:"time"`
	AccountID string          `json:"account_id"`
	Data      json.RawMessage `json:"data"`
	Error     *errorBody      `json:"error"`
	DelayMS   int64           `json:"delay_ms"`
	Exit      *int            `json:"exit"`
}

// runStream plays a scenario's `stream marketdata` script as NDJSON. It honors
// context cancellation (the robot's cancel -> SIGTERM shutdown) by emitting a
// final "disconnected" shutdown frame and exiting 0, matching the real CLI.
func runStream(ctx context.Context, s *scenario, w io.Writer) int {
	if s.Stream.Script == "" {
		_ = writeFrame(w, s.errorFrame("INTERNAL", "scenario configures no stream script", ""))
		return exitInternal
	}
	entries, err := loadScript(filepath.Join(s.dir, s.Stream.Script))
	if err != nil {
		_ = writeFrame(w, s.errorFrame("INTERNAL", err.Error(), ""))
		return exitInternal
	}

	for _, e := range entries {
		if ctx.Err() != nil {
			return s.emitShutdown(w)
		}
		if e.DelayMS > 0 {
			select {
			case <-ctx.Done():
				return s.emitShutdown(w)
			case <-time.After(time.Duration(e.DelayMS) * time.Millisecond):
			}
		}
		if e.Type != "" || e.Error != nil {
			frame := streamFrame{
				Type:          e.Type,
				SchemaVersion: s.SchemaVersion,
				Time:          firstNonEmpty(e.Time, s.Stream.ShutdownTime, defaultStreamTime),
				AccountID:     e.AccountID,
				Data:          e.Data,
				Error:         e.Error,
			}
			if err := writeFrame(w, frame); err != nil {
				fmt.Fprintf(os.Stderr, "faketinvest: write stream frame: %v\n", err)
				return exitInternal
			}
		}
		if e.Exit != nil {
			return *e.Exit
		}
	}
	return exitOK
}

// emitShutdown writes the clean-shutdown lifecycle frame and returns exit 0.
func (s *scenario) emitShutdown(w io.Writer) int {
	frame := streamFrame{
		Type:          "disconnected",
		SchemaVersion: s.SchemaVersion,
		Time:          firstNonEmpty(s.Stream.ShutdownTime, defaultStreamTime),
		Data:          json.RawMessage(`{"reason":"shutdown","final":true}`),
	}
	if err := writeFrame(w, frame); err != nil {
		fmt.Fprintf(os.Stderr, "faketinvest: write shutdown frame: %v\n", err)
		return exitInternal
	}
	return exitOK
}

// errorFrame builds a stream error frame.
func (s *scenario) errorFrame(code, message, accountID string) streamFrame {
	return streamFrame{
		Type:          "error",
		SchemaVersion: s.SchemaVersion,
		Time:          firstNonEmpty(s.Stream.ShutdownTime, defaultStreamTime),
		AccountID:     accountID,
		Error:         &errorBody{Code: code, Message: message},
	}
}

// loadScript reads a stream script as either a JSON array of entries or a
// newline-delimited (NDJSON) list of entries.
func loadScript(path string) ([]scriptEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read stream script: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var entries []scriptEntry
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("decode stream script array: %w", err)
		}
		return entries, nil
	}
	var entries []scriptEntry
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for {
		var e scriptEntry
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode stream script line: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
