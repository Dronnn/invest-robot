package tinvestcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// Exit codes are tinvest's stable contract (DESIGN §4, AGENTS.md exit table).
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

// wireEnvelope is the uniform JSON wrapper for every unary command result. It
// mirrors the real render.Envelope field for field.
type wireEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *wireErrorBody  `json:"error"`
	Meta  wireMeta        `json:"meta"`
}

// wireMeta is the envelope metadata attached to every response.
type wireMeta struct {
	AccountID     string `json:"account_id"`
	TrackingID    string `json:"tracking_id"`
	ElapsedMS     int64  `json:"elapsed_ms"`
	Contract      string `json:"contract"`
	SchemaVersion string `json:"schema_version"`
}

// wireErrorBody is the machine-readable error block of a failure envelope.
type wireErrorBody struct {
	Code          string             `json:"code"`
	GRPCCode      string             `json:"grpc_code"`
	APICode       string             `json:"api_code"`
	Message       string             `json:"message"`
	Retryable     bool               `json:"retryable"`
	RetryAfterMS  int64              `json:"retry_after_ms"`
	Phase         string             `json:"phase"`
	TrackingID    string             `json:"tracking_id"`
	Details       map[string]string  `json:"details"`
	ReconcileHint *wireReconcileHint `json:"reconcile"`
}

type wireReconcileHint struct {
	OrderID string `json:"order_id"`
	Command string `json:"command"`
}

func (b *wireErrorBody) broker() BrokerError {
	if b == nil {
		return BrokerError{}
	}
	return BrokerError{
		Code:         b.Code,
		GRPCCode:     b.GRPCCode,
		APICode:      b.APICode,
		Message:      b.Message,
		Retryable:    b.Retryable,
		RetryAfterMS: b.RetryAfterMS,
		Phase:        b.Phase,
		TrackingID:   b.TrackingID,
		Details:      b.Details,
	}
}

// parseEnvelope decodes exactly one JSON envelope from raw. It rejects empty
// input, leading garbage, truncation, and any trailing bytes beyond the first
// value (a second concatenated envelope) as a ProtocolError — the envelope must
// be a single well-formed value or it is not a broker answer at all.
func parseEnvelope(raw []byte) (*wireEnvelope, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var env wireEnvelope
	if err := dec.Decode(&env); err != nil {
		reason := "malformed envelope JSON"
		if errors.Is(err, io.EOF) {
			reason = "empty envelope"
		} else if errors.Is(err, io.ErrUnexpectedEOF) {
			reason = "truncated envelope JSON"
		}
		return nil, &ProtocolError{Reason: reason, Err: err}
	}
	// Anything after the first value — even a second complete envelope — means
	// the output is not a single trustworthy answer.
	if dec.More() {
		return nil, &ProtocolError{Reason: "multiple JSON values in envelope output"}
	}
	// Drain trailing whitespace; a non-whitespace token here is also junk.
	if _, err := dec.Token(); err != nil && !errors.Is(err, io.EOF) {
		return nil, &ProtocolError{Reason: "trailing bytes after envelope", Err: err}
	}
	return &env, nil
}

// classify turns a completed process result (exit code + stdout envelope) into
// either the decoded data payload or one typed error, plus the parsed meta.
// When checkSchema is true the envelope schema_version must be in
// schemaAllowlist — enforced on every ordinary call so a binary that drifts
// under a running robot is caught, not trusted (the handshake disables it to
// report its own HandshakeError instead). allowReconcileExit1 enables the one
// documented exception where `orders reconcile` reports ok:true yet exits 1
// because intents remain unresolved.
func classify(exit int, stdout []byte, schemaAllowlist []string, checkSchema, allowReconcileExit1 bool) (json.RawMessage, wireMeta, error) {
	env, err := parseEnvelope(stdout)
	if err != nil {
		return nil, wireMeta{}, err
	}
	meta := env.Meta

	// The schema is contract-critical: an unknown version means we cannot trust
	// any field, regardless of exit code.
	if checkSchema && !slices.Contains(schemaAllowlist, env.Meta.SchemaVersion) {
		return nil, meta, &ProtocolError{
			Reason: "unknown envelope schema_version",
			Detail: fmt.Sprintf("got %q, allow %v", env.Meta.SchemaVersion, schemaAllowlist),
		}
	}

	broker := env.Error.broker()

	// ok:false must carry an error body, and vice-versa: ok:true must not.
	if !env.OK && env.Error == nil {
		return nil, meta, &ProtocolError{Reason: "ok:false without an error body"}
	}
	if env.OK && env.Error != nil {
		return nil, meta, &ProtocolError{Reason: "ok:true with an error body"}
	}

	switch exit {
	case exitOK:
		if !env.OK {
			return nil, meta, &ProtocolError{Reason: "ok:false with exit 0"}
		}
		return env.Data, meta, nil

	case exitInternal:
		// Documented exception: reconcile stays ok:true but exits 1 while any
		// intent is still unresolved. The caller reads unresolved_count.
		if allowReconcileExit1 && env.OK {
			return env.Data, meta, nil
		}
		if env.OK {
			return nil, meta, &ProtocolError{Reason: "ok:true with exit 1"}
		}
		return nil, meta, &InternalError{BrokerError: broker}

	case exitUsage:
		if env.OK {
			return nil, meta, contradiction(exit)
		}
		if broker.Code == "POLICY" {
			return nil, meta, &PolicyError{BrokerError: broker}
		}
		return nil, meta, &UsageError{BrokerError: broker}

	case exitAuth:
		if env.OK {
			return nil, meta, contradiction(exit)
		}
		return nil, meta, &AuthError{BrokerError: broker}

	case exitRateLimited:
		if env.OK {
			return nil, meta, contradiction(exit)
		}
		return nil, meta, &RateLimitError{
			BrokerError: broker,
			RetryAfter:  time.Duration(broker.RetryAfterMS) * time.Millisecond,
		}

	case exitRejected:
		if env.OK {
			return nil, meta, contradiction(exit)
		}
		return nil, meta, &BrokerRejectedError{BrokerError: broker}

	case exitNetwork:
		if env.OK {
			return nil, meta, contradiction(exit)
		}
		return nil, meta, &NetworkError{BrokerError: broker}

	case exitUnconfirmed:
		if env.OK {
			return nil, meta, contradiction(exit)
		}
		hint := ReconcileHint{}
		if env.Error != nil && env.Error.ReconcileHint != nil {
			hint = ReconcileHint{
				OrderID: env.Error.ReconcileHint.OrderID,
				Command: env.Error.ReconcileHint.Command,
			}
		}
		return nil, meta, &OutcomeUnknownError{BrokerError: broker, ReconcileHint: hint}

	default:
		return nil, meta, &ProtocolError{
			Reason: "exit code outside the documented 0..7 contract",
			Detail: fmt.Sprintf("exit %d", exit),
		}
	}
}

func contradiction(exit int) *ProtocolError {
	return &ProtocolError{
		Reason: "ok:true with a non-zero error exit code",
		Detail: fmt.Sprintf("exit %d", exit),
	}
}
