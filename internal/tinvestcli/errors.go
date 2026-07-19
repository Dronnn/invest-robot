package tinvestcli

import (
	"fmt"
	"time"
)

// The error taxonomy at the tinvest boundary. Every failed call returns exactly
// one of these concrete types so callers branch with errors.As, never on
// human-readable message text (DESIGN §4, §12). The exit-code → type mapping is:
//
//	1 → InternalError
//	2 → UsageError (error.code "USAGE") | PolicyError (error.code "POLICY")
//	3 → AuthError
//	4 → RateLimitError
//	5 → BrokerRejectedError
//	6 → NetworkError
//	7 → OutcomeUnknownError
//
// ProtocolError is orthogonal to the exit code: it means the envelope itself is
// unusable (malformed, truncated, multiple JSON values, an ok/exit-code
// contradiction, an unknown schema_version, or ok:false with exit 0). A
// ProtocolError is a bug in the contract or the binary, never a broker answer.

// BrokerError is the machine-readable error block carried by a failure
// envelope. It is embedded in the exit-code error types so the code, tracking
// id, phase and details survive to the caller for logging. Retry decisions are
// driven by the error's Go type, never by Retryable or Message.
type BrokerError struct {
	Code         string            // render error code: USAGE, POLICY, AUTH, RATE_LIMITED, ...
	GRPCCode     string            // optional upstream gRPC status
	APICode      string            // optional T-Bank API code
	Message      string            // human-readable; for display/logging only
	Retryable    bool              // the CLI's own hint; always present in the envelope
	RetryAfterMS int64             // populated for RATE_LIMITED
	Phase        string            // e.g. "sent_unconfirmed"
	TrackingID   string            // upstream request id
	Details      map[string]string // optional structured detail
}

func (e BrokerError) String() string {
	if e.Code == "" {
		return e.Message
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// UsageError is exit 2 with error.code "USAGE": the robot built an invalid
// command (a bug), never a broker outcome.
type UsageError struct{ BrokerError }

func (e *UsageError) Error() string { return "tinvestcli: usage error: " + e.BrokerError.String() }

// PolicyError is exit 2 with error.code "POLICY": a guardrail in tinvest's
// policy file rejected the request (e.g. instrument not on the allowlist).
type PolicyError struct{ BrokerError }

func (e *PolicyError) Error() string {
	return "tinvestcli: policy violation: " + e.BrokerError.String()
}

// AuthError is exit 3: authentication or permission failure. The caller must
// stop trading and surface a prominent alert (DESIGN §12).
type AuthError struct{ BrokerError }

func (e *AuthError) Error() string { return "tinvestcli: auth failure: " + e.BrokerError.String() }

// RateLimitError is exit 4: the client-side rate limiter tripped. RetryAfter is
// the honored delay derived from error.retry_after_ms (zero if the CLI gave
// none). Only read calls retry on this; mutations surface it.
type RateLimitError struct {
	BrokerError
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("tinvestcli: rate limited (retry after %s): %s", e.RetryAfter, e.BrokerError.String())
}

// BrokerRejectedError is exit 5: the broker rejected the request. The outcome
// is final and confirmed — never blindly retried.
type BrokerRejectedError struct{ BrokerError }

func (e *BrokerRejectedError) Error() string {
	return "tinvestcli: broker rejected: " + e.BrokerError.String()
}

// NetworkError is exit 6 (or a local per-call timeout): a transient transport
// failure. Read calls retry it with bounded attempts; mutations surface it so
// the caller can decide (the send may or may not have reached the broker).
type NetworkError struct {
	BrokerError
	// Timeout is true when this NetworkError was synthesized from the adapter's
	// own per-call deadline rather than a CLI exit 6.
	Timeout bool
}

func (e *NetworkError) Error() string {
	if e.Timeout {
		return "tinvestcli: call timed out: " + e.BrokerError.String()
	}
	return "tinvestcli: network failure: " + e.BrokerError.String()
}

// ReconcileHint is the recovery instruction attached to an outcome-unknown
// mutation: run Command (normally "tinvest orders reconcile") for OrderID before
// any retry.
type ReconcileHint struct {
	OrderID string
	Command string
}

// OutcomeUnknownError is exit 7: a mutation was sent but its outcome is unknown.
// The order must be frozen and reconciled (via ReconcileHint.Command) before any
// further mutation (DESIGN §4). Never retried.
type OutcomeUnknownError struct {
	BrokerError
	ReconcileHint ReconcileHint
}

func (e *OutcomeUnknownError) Error() string {
	return "tinvestcli: outcome unknown, reconcile required: " + e.BrokerError.String()
}

// InternalError is exit 1: tinvest failed internally (and, for non-reconcile
// commands, an exit that does not match any other class).
type InternalError struct{ BrokerError }

func (e *InternalError) Error() string {
	return "tinvestcli: internal error: " + e.BrokerError.String()
}

// ProtocolError means the envelope could not be trusted as a broker answer:
// malformed or truncated JSON, more than one JSON value, an exit code that
// contradicts the ok flag, ok:false with exit 0, or an unknown schema_version.
// It signals a contract or binary bug, not a trading outcome.
type ProtocolError struct {
	// Reason is a stable, machine-set description of the violation.
	Reason string
	// Detail carries additional context (e.g. the offending exit code).
	Detail string
	// Err is the wrapped decode error when the violation was a JSON failure.
	Err error
}

func (e *ProtocolError) Error() string {
	msg := "tinvestcli: protocol error: " + e.Reason
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ProtocolError) Unwrap() error { return e.Err }

// ResolveError is returned by Resolve when the tinvest binary cannot be located
// or hashed. It is a setup failure, distinct from any broker outcome.
type ResolveError struct {
	Path string
	Err  error
}

func (e *ResolveError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("tinvestcli: resolve %q: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("tinvestcli: resolve tinvest: %v", e.Err)
}

func (e *ResolveError) Unwrap() error { return e.Err }

// HandshakeError is returned by Handshake when the resolved binary's version
// envelope fails the startup contract check (schema_version outside the
// allowlist, or an internally inconsistent version payload). The robot refuses
// to start on this error (DESIGN §4).
type HandshakeError struct {
	Reason string
	Info   Info
}

func (e *HandshakeError) Error() string {
	return "tinvestcli: handshake rejected: " + e.Reason
}
