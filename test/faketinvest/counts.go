package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// countsFile is the per-command invocation counter, persisted so that failure
// injection "after K calls" works across the separate process spawns the robot
// makes for each unary call. It lives in the state directory
// (FAKETINVEST_STATE, else the scenario directory).
const countsFile = ".faketinvest-counts.json"

// stateDir returns the directory used for the persistent call counter.
func stateDir(env envLookup, scenarioDir string) string {
	if dir := env("FAKETINVEST_STATE"); dir != "" {
		return dir
	}
	return scenarioDir
}

// bumpCount increments and returns the 1-based invocation count for a command
// key. A read-modify-write of a small JSON file is enough: the robot serializes
// unary calls per method group, and the fake's own tests run sequentially.
func bumpCount(dir, command string) int {
	path := filepath.Join(dir, countsFile)
	counts := map[string]int{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &counts)
	}
	counts[command]++
	n := counts[command]
	if raw, err := json.MarshalIndent(counts, "", "  "); err == nil {
		_ = os.WriteFile(path, raw, 0o644)
	}
	return n
}

// matchFailure returns the failure rule that applies to this command on this
// invocation number, or nil. A rule with OnCall <= 0 fires on every matching
// call; otherwise it fires only on the OnCall-th matching call.
func matchFailure(rules []failSpec, command string, call int) *failSpec {
	for i := range rules {
		r := &rules[i]
		if r.Command != command {
			continue
		}
		if r.OnCall <= 0 || r.OnCall == call {
			return r
		}
	}
	return nil
}

// toError converts a failure rule into an error envelope body plus its exit
// code. orderID supplies the reconcile order id when the rule does not pin one.
func (r *failSpec) toError(orderID string) (*errorBody, int) {
	body := &errorBody{
		Code:         r.Code,
		Message:      r.Message,
		Retryable:    r.Retryable,
		RetryAfterMS: r.RetryAfterMS,
		Phase:        r.Phase,
	}
	if r.Code == "UNCONFIRMED" || r.ReconcileCommand != "" {
		id := r.ReconcileOrderID
		if id == "" {
			id = orderID
		}
		body.ReconcileHint = &reconcileHint{OrderID: id, Command: r.ReconcileCommand}
	}
	exit := exitForCode(r.Code)
	if r.Exit != nil {
		exit = *r.Exit
	}
	return body, exit
}
