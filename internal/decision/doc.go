// Package decision defines the engine-agnostic decision contract: the
// Request every decision engine receives, the Response every engine
// returns, the Engine port itself, and the two validation layers (shape,
// semantics) that gate a Response before risk-check and execution.
//
// This package is pure: standard library plus internal/model and
// internal/features only, no I/O. That is what lets every engine
// implementation (rules, claude-cli, anthropic-api) and the cycle
// orchestrator depend on it without pulling in infrastructure, and what
// makes a Request replayable byte-for-byte from what was persisted (see
// DESIGN.md §5's "replay = re-reading cycles + llm_calls + decisions").
//
// Request is deliberately self-contained: everything an engine needs to
// decide — portfolio, open intents, per-instrument market/feature context,
// configured risk limits, recent outcomes — travels in one value with a
// stable JSON shape, because this exact struct is what gets serialized into
// LLM prompts and persisted in llm_calls.request. See DESIGN.md §6 (cycle)
// and §8 (engines) for the surrounding contract.
package decision
