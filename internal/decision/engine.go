package decision

import "context"

// Engine is the pluggable decision-engine port every implementation (rules,
// claude-cli, anthropic-api) satisfies, per DESIGN.md §8's common contract.
// Decide is a function of req alone from the engine's point of view: no
// engine reads broker state or places orders itself. Risk-check and
// execution happen strictly after Decide returns, in the cycle state
// machine — an engine response can tighten risk limits but never widen
// them; risk always has the last word.
type Engine interface {
	// Name identifies the engine (e.g. "rules", "claude-cli").
	Name() string
	// Version identifies this engine's behavior version (e.g. "rules/v1"),
	// stored per cycle so a decision can be attributed to the exact logic
	// that produced it.
	Version() string
	// Decide computes a Response for req. ctx carries the cycle's wall-clock
	// budget; an engine must respect cancellation.
	Decide(ctx context.Context, req Request) (Response, Meta, error)
}

// Meta is engine-call metadata persisted alongside every Decide invocation
// (the llm_calls table, DESIGN.md §5), independent of engine kind.
type Meta struct {
	// DurationMS is how long Decide took to return.
	DurationMS int64 `json:"duration_ms"`
	// Raw is the engine-native output as persisted evidence: for rules, the
	// marshaled Response itself; for claude-cli/anthropic-api, the raw
	// subprocess/HTTP response body captured before schema validation.
	Raw []byte `json:"raw,omitempty"`
	// Model names the underlying model when the engine is LLM-backed (e.g.
	// "claude-sonnet-5"); empty for the deterministic rules engine.
	Model string `json:"model,omitempty"`
}
