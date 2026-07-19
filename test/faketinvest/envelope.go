package main

import (
	"bytes"
	"encoding/json"
	"io"
)

// The envelope and stream frame types below mirror, field for field, the real
// tinvest render package (internal/render/envelope.go and stream.go). Keeping
// the Go struct field order identical to the real CLI means the fake's emitted
// JSON key order matches the real binary's, so captured golden fixtures stay
// interchangeable between the two.

// meta is the envelope metadata attached to every unary response.
type meta struct {
	AccountID     string `json:"account_id,omitempty"`
	TrackingID    string `json:"tracking_id,omitempty"`
	ElapsedMS     int64  `json:"elapsed_ms"`
	Contract      string `json:"contract"`
	SchemaVersion string `json:"schema_version"`
}

// envelope is the uniform JSON wrapper for every unary command result.
type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *errorBody      `json:"error,omitempty"`
	Meta  meta            `json:"meta"`
}

// errorBody is the machine-readable error block of a failure envelope.
type errorBody struct {
	Code          string            `json:"code"`
	GRPCCode      string            `json:"grpc_code,omitempty"`
	APICode       string            `json:"api_code,omitempty"`
	Message       string            `json:"message"`
	Retryable     bool              `json:"retryable"`
	RetryAfterMS  int64             `json:"retry_after_ms,omitempty"`
	Phase         string            `json:"phase,omitempty"`
	TrackingID    string            `json:"tracking_id,omitempty"`
	Details       map[string]string `json:"details,omitempty"`
	ReconcileHint *reconcileHint    `json:"reconcile,omitempty"`
}

// reconcileHint is the recovery instruction attached to an UNCONFIRMED (exit 7)
// mutation.
type reconcileHint struct {
	OrderID string `json:"order_id,omitempty"`
	Command string `json:"command"`
}

// writeEnvelope renders one envelope as 2-space-indented JSON followed by a
// newline — identical framing to render.WriteJSON (json.Encoder.SetIndent).
// MarshalIndent re-indents the embedded Data RawMessage in place, so a fixture's
// own field order is preserved rather than being alphabetized.
func writeEnvelope(w io.Writer, env envelope) error {
	buf, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = w.Write(buf)
	return err
}

// streamFrame is the common NDJSON frame for stream commands. Field order is
// intentional: type is the first key on every line.
type streamFrame struct {
	Type          string          `json:"type"`
	SchemaVersion string          `json:"schema_version"`
	Time          string          `json:"time"`
	AccountID     string          `json:"account_id,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
	Error         *errorBody      `json:"error,omitempty"`
}

// writeFrame writes one compact JSON object per line, matching the real
// NDJSONWriter (json.Encoder without indentation, flushed per line).
func writeFrame(w io.Writer, frame streamFrame) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(frame); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// exitForCode maps a render error code to the process exit code, exactly as the
// real CLIError.ExitCode does.
func exitForCode(code string) int {
	switch code {
	case "USAGE", "POLICY":
		return exitUsage
	case "AUTH":
		return exitAuth
	case "RATE_LIMITED":
		return exitRateLimited
	case "BROKER_REJECTED":
		return exitRejected
	case "NETWORK":
		return exitNetwork
	case "UNCONFIRMED":
		return exitUnconfirmed
	default:
		return exitInternal
	}
}

// Exit codes are a stable contract (AGENTS.md exit table).
const (
	exitOK          = 0
	exitInternal    = 1
	exitUsage       = 2
	exitAuth        = 3
	exitRateLimited = 4
	exitRejected    = 5
	exitNetwork     = 6
	exitUnconfirmed = 7
)

// Contract constants pinned to the real render package.
const (
	defaultSchemaVersion = "0.1"
	defaultContract      = "1.49"
	defaultVersion       = "0.1.0"
	defaultGoVersion     = "go1.26"
)
