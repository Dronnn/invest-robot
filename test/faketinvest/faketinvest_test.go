package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden files under testdata/")

const (
	happyDir   = "scenarios/happy"
	hostileDir = "scenarios/hostile"
)

// Client order ids must be canonical UUIDs — the real CLI rejects anything
// else with a usage error (validateOrderID in orders.go). These match the
// constants internal/tinvestcli/client_test.go uses for the same reason.
const (
	orderUUID1 = "550e8400-e29b-41d4-a716-446655440000"
	orderUUID2 = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

// invoke runs the fake in-process with the given argv and environment overrides,
// returning the exit code and captured stdout. A per-call state dir keeps the
// committed scenarios free of the call-counter file.
func invoke(t *testing.T, envOverrides map[string]string, argv ...string) (int, string) {
	t.Helper()
	env := map[string]string{"FAKETINVEST_STATE": t.TempDir()}
	for k, v := range envOverrides {
		env[k] = v
	}
	lookup := func(key string) string { return env[key] }
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), argv, lookup, &stdout, &stderr)
	if stderr.Len() > 0 {
		t.Logf("stderr: %s", stderr.String())
	}
	return code, stdout.String()
}

// scenarioEnv points the fake at a scenario and shares one state dir across a
// sequence of calls (needed for call-count-based failure injection).
func scenarioEnv(dir, stateDir string) map[string]string {
	return map[string]string{"FAKETINVEST_SCENARIO": dir, "FAKETINVEST_STATE": stateDir}
}

func TestGoldenEnvelopes(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"version", []string{"version", "-o", "json"}},
		{"instruments_get", []string{"instruments", "get", "SBER@TQBR", "-o", "json"}},
		{"instruments_search", []string{"instruments", "search", "gaz", "-o", "json"}},
		{"quotes_last", []string{"quotes", "last", "SBER@TQBR", "GAZP@TQBR", "-o", "json"}},
		{"candles_get", []string{"candles", "get", "SBER@TQBR", "--interval", "5m", "--from", "2026-07-19T09:00:00Z", "--to", "2026-07-19T09:20:00Z", "-o", "json"}},
		{"orderbook_get", []string{"orderbook", "get", "SBER@TQBR", "--depth", "10", "-o", "json"}},
		{"portfolio_get", []string{"portfolio", "get", "-o", "json"}},
		{"positions_get", []string{"positions", "get", "-o", "json"}},
		{"operations_list", []string{"operations", "list", "-o", "json"}},
		{"orders_place", []string{"orders", "place", "--instrument", "SBER@TQBR", "--direction", "buy", "--quantity", "1", "--type", "limit", "--price", "270.5", "--order-id", orderUUID1, "-o", "json"}},
		{"orders_list", []string{"orders", "list", "-o", "json"}},
		{"orders_cancel", []string{"orders", "cancel", "ord-happy-1", "-o", "json"}},
		{"stop_orders_list", []string{"stop-orders", "list", "-o", "json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": happyDir}, tc.argv...)
			if code != exitOK {
				t.Fatalf("exit = %d, want 0; output:\n%s", code, out)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("output is not valid JSON:\n%s", out)
			}
			assertEnvelopeContract(t, out, true)
			checkGolden(t, tc.name, out)
		})
	}
}

// assertEnvelopeContract verifies the invariants every unary envelope must hold:
// ok flag, presence of meta, and the pinned schema_version/contract.
func assertEnvelopeContract(t *testing.T, out string, wantOK bool) {
	t.Helper()
	var env struct {
		OK   bool `json:"ok"`
		Meta struct {
			Contract      string `json:"contract"`
			SchemaVersion string `json:"schema_version"`
		} `json:"meta"`
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.OK != wantOK {
		t.Errorf("ok = %v, want %v", env.OK, wantOK)
	}
	if env.Meta.SchemaVersion != defaultSchemaVersion {
		t.Errorf("meta.schema_version = %q, want %q", env.Meta.SchemaVersion, defaultSchemaVersion)
	}
	if env.Meta.Contract != defaultContract {
		t.Errorf("meta.contract = %q, want %q", env.Meta.Contract, defaultContract)
	}
	if wantOK && len(env.Data) == 0 {
		t.Error("ok envelope has no data")
	}
	if !wantOK && len(env.Error) == 0 {
		t.Error("failure envelope has no error")
	}
}

func TestExitCodeInjection(t *testing.T) {
	t.Run("rate_limited_on_second_call", func(t *testing.T) {
		state := t.TempDir()
		env := scenarioEnv(hostileDir, state)
		if code, _ := invoke(t, env, "quotes", "last", "SBER@TQBR", "-o", "json"); code != exitOK {
			t.Fatalf("first quotes last exit = %d, want 0", code)
		}
		code, out := invoke(t, env, "quotes", "last", "SBER@TQBR", "-o", "json")
		if code != exitRateLimited {
			t.Fatalf("second quotes last exit = %d, want %d", code, exitRateLimited)
		}
		assertEnvelopeContract(t, out, false)
		body := decodeError(t, out)
		if body.Code != "RATE_LIMITED" {
			t.Errorf("code = %q, want RATE_LIMITED", body.Code)
		}
		if body.RetryAfterMS != 1500 {
			t.Errorf("retry_after_ms = %d, want 1500", body.RetryAfterMS)
		}
		if !body.Retryable {
			t.Error("retryable = false, want true")
		}
	})

	t.Run("network_on_first_candle_read", func(t *testing.T) {
		code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": hostileDir},
			"candles", "get", "SBER@TQBR", "--interval", "5m", "--from", "2026-07-19T09:00:00Z", "--to", "2026-07-19T09:20:00Z", "-o", "json")
		if code != exitNetwork {
			t.Fatalf("exit = %d, want %d", code, exitNetwork)
		}
		if decodeError(t, out).Code != "NETWORK" {
			t.Errorf("code = %q, want NETWORK", decodeError(t, out).Code)
		}
	})

	t.Run("unconfirmed_order_carries_reconcile_hint", func(t *testing.T) {
		code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": hostileDir},
			"orders", "place", "--instrument", "SBER@TQBR", "--direction", "buy", "--quantity", "1", "--type", "market", "--order-id", orderUUID2, "-o", "json")
		if code != exitUnconfirmed {
			t.Fatalf("exit = %d, want %d", code, exitUnconfirmed)
		}
		body := decodeError(t, out)
		if body.Code != "UNCONFIRMED" {
			t.Errorf("code = %q, want UNCONFIRMED", body.Code)
		}
		if body.ReconcileHint == nil {
			t.Fatal("missing reconcile hint")
		}
		if body.ReconcileHint.OrderID != orderUUID2 {
			t.Errorf("reconcile.order_id = %q, want %s", body.ReconcileHint.OrderID, orderUUID2)
		}
		if body.ReconcileHint.Command == "" {
			t.Error("reconcile.command is empty")
		}
	})

	t.Run("reconcile_resolves_clean_exit_zero", func(t *testing.T) {
		code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": hostileDir}, "orders", "reconcile", "-o", "json")
		if code != exitOK {
			t.Fatalf("exit = %d, want 0; out:\n%s", code, out)
		}
		assertEnvelopeContract(t, out, true)
	})
}

func TestUsageAndRejection(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		wantExit int
		wantCode string
	}{
		{"invalid_output_format", []string{"version", "-o", "yaml"}, exitUsage, "USAGE"},
		{"malformed_instrument_id", []string{"instruments", "get", "BAD!!ID", "-o", "json"}, exitUsage, "USAGE"},
		{"unknown_instrument", []string{"instruments", "get", "NOPE@TQBR", "-o", "json"}, exitRejected, "BROKER_REJECTED"},
		{"unknown_flag", []string{"quotes", "last", "SBER@TQBR", "--bogus", "wat", "-o", "json"}, exitUsage, "USAGE"},
		{"flag_wrong_for_command", []string{"orders", "list", "--interval", "5m", "-o", "json"}, exitUsage, "USAGE"},
		{"missing_flag_value", []string{"quotes", "last", "SBER@TQBR", "-o", "json", "--account"}, exitUsage, "USAGE"},
		{"order_id_not_a_uuid", []string{"orders", "place", "--instrument", "SBER@TQBR", "--direction", "buy", "--quantity", "1", "--type", "market", "--order-id", "not-a-uuid", "-o", "json"}, exitUsage, "USAGE"},
		{"invalid_direction", []string{"orders", "place", "--instrument", "SBER@TQBR", "--direction", "up", "--quantity", "1", "--type", "market", "--order-id", orderUUID1, "-o", "json"}, exitUsage, "USAGE"},
		{"invalid_order_type", []string{"orders", "place", "--instrument", "SBER@TQBR", "--direction", "buy", "--quantity", "1", "--type", "stop", "--order-id", orderUUID1, "-o", "json"}, exitUsage, "USAGE"},
		{"non_positive_quantity", []string{"orders", "place", "--instrument", "SBER@TQBR", "--direction", "buy", "--quantity", "0", "--type", "market", "--order-id", orderUUID1, "-o", "json"}, exitUsage, "USAGE"},
		{"limit_requires_price", []string{"orders", "place", "--instrument", "SBER@TQBR", "--direction", "buy", "--quantity", "1", "--type", "limit", "--order-id", orderUUID1, "-o", "json"}, exitUsage, "USAGE"},
		{"price_not_allowed_for_market", []string{"orders", "place", "--instrument", "SBER@TQBR", "--direction", "buy", "--quantity", "1", "--type", "market", "--price", "270.5", "--order-id", orderUUID1, "-o", "json"}, exitUsage, "USAGE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": happyDir}, tc.argv...)
			if code != tc.wantExit {
				t.Fatalf("exit = %d, want %d; out:\n%s", code, tc.wantExit, out)
			}
			assertEnvelopeContract(t, out, false)
			if got := decodeError(t, out).Code; got != tc.wantCode {
				t.Errorf("code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

// TestReconcileUnresolvedExit checks the AGENTS.md rule: reconcile stays
// ok:true but exits 1 when any intent is still unresolved.
func TestReconcileUnresolvedExit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scenario.toml"), `
account_id = "acc"
[responses]
orders_reconcile = "reconcile.json"
`)
	writeFile(t, filepath.Join(dir, "reconcile.json"),
		`{"outcomes":[{"intent_id":"i1","outcome":"indeterminate"}],"unresolved_count":1}`)

	code, out := invoke(t, map[string]string{"FAKETINVEST_SCENARIO": dir}, "orders", "reconcile", "-o", "json")
	if code != exitInternal {
		t.Fatalf("exit = %d, want %d (unresolved reconcile)", code, exitInternal)
	}
	assertEnvelopeContract(t, out, true) // still ok:true
}

func decodeError(t *testing.T, out string) *errorBody {
	t.Helper()
	var env struct {
		Error *errorBody `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error == nil {
		t.Fatalf("no error block in:\n%s", out)
	}
	return env.Error
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	if *update {
		writeFile(t, path, got)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run: go test ./test/faketinvest -update): %v", path, err)
	}
	if strings.TrimRight(string(want), "\n") != strings.TrimRight(got, "\n") {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
