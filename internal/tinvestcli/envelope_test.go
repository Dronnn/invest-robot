package tinvestcli

import (
	"errors"
	"testing"
	"time"
)

var allow01 = []string{"0.1"}

const okMeta = `"meta":{"elapsed_ms":0,"contract":"1.49","schema_version":"0.1"}`

func TestParseEnvelopeProtocolErrors(t *testing.T) {
	cases := map[string]string{
		"garbage":            "this is not json",
		"empty":              "",
		"whitespace only":    "   \n  ",
		"truncated":          `{"ok":true,"data":{"x":1`,
		"multiple envelopes": `{"ok":true,` + okMeta + `}` + "\n" + `{"ok":true,` + okMeta + `}`,
		"trailing garbage":   `{"ok":true,` + okMeta + `} trailing`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseEnvelope([]byte(in))
			var pe *ProtocolError
			if !errors.As(err, &pe) {
				t.Fatalf("want *ProtocolError, got %T: %v", err, err)
			}
		})
	}
}

func TestParseEnvelopeValid(t *testing.T) {
	in := `{"ok":true,"data":{"a":1},` + okMeta + `}` + "\n"
	env, err := parseEnvelope([]byte(in))
	if err != nil {
		t.Fatalf("parseEnvelope: %v", err)
	}
	if !env.OK || env.Meta.SchemaVersion != "0.1" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestClassifyProtocolContradictions(t *testing.T) {
	cases := map[string]struct {
		exit int
		body string
	}{
		"ok false with exit 0":        {exit: 0, body: `{"ok":false,"error":{"code":"X","message":"m","retryable":false},` + okMeta + `}`},
		"ok true with error exit":     {exit: 6, body: `{"ok":true,"data":{},` + okMeta + `}`},
		"ok false without error":      {exit: 1, body: `{"ok":false,` + okMeta + `}`},
		"ok true with error body":     {exit: 0, body: `{"ok":true,"data":{},"error":{"code":"X","message":"m","retryable":false},` + okMeta + `}`},
		"ok true with exit 1 non-rec": {exit: 1, body: `{"ok":true,"data":{},` + okMeta + `}`},
		"exit outside contract":       {exit: 9, body: `{"ok":false,"error":{"code":"X","message":"m","retryable":false},` + okMeta + `}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := classify(tc.exit, []byte(tc.body), allow01, true, false)
			var pe *ProtocolError
			if !errors.As(err, &pe) {
				t.Fatalf("want *ProtocolError, got %T: %v", err, err)
			}
		})
	}
}

func TestClassifyUnknownSchema(t *testing.T) {
	body := `{"ok":true,"data":{},"meta":{"contract":"1.49","schema_version":"0.2"}}`
	_, _, err := classify(0, []byte(body), allow01, true, false)
	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("want *ProtocolError for unknown schema, got %T: %v", err, err)
	}
	// With the schema check disabled (the handshake path), the same body is
	// accepted so the caller can render its own HandshakeError.
	if _, _, err := classify(0, []byte(body), allow01, false, false); err != nil {
		t.Fatalf("skipSchema classify: unexpected error %v", err)
	}
}

func TestClassifyExitCodeMapping(t *testing.T) {
	errBody := func(code string, extra string) string {
		e := `"code":"` + code + `","message":"boom","retryable":true`
		if extra != "" {
			e += "," + extra
		}
		return `{"ok":false,"error":{` + e + `},` + okMeta + `}`
	}

	t.Run("usage", func(t *testing.T) {
		_, _, err := classify(2, []byte(errBody("USAGE", "")), allow01, true, false)
		var target *UsageError
		if !errors.As(err, &target) {
			t.Fatalf("want *UsageError, got %T", err)
		}
	})
	t.Run("policy", func(t *testing.T) {
		_, _, err := classify(2, []byte(errBody("POLICY", "")), allow01, true, false)
		var target *PolicyError
		if !errors.As(err, &target) {
			t.Fatalf("want *PolicyError, got %T", err)
		}
	})
	t.Run("auth", func(t *testing.T) {
		_, _, err := classify(3, []byte(errBody("AUTH", "")), allow01, true, false)
		var target *AuthError
		if !errors.As(err, &target) {
			t.Fatalf("want *AuthError, got %T", err)
		}
	})
	t.Run("rate limited carries retry after", func(t *testing.T) {
		_, _, err := classify(4, []byte(errBody("RATE_LIMITED", `"retry_after_ms":1500`)), allow01, true, false)
		var target *RateLimitError
		if !errors.As(err, &target) {
			t.Fatalf("want *RateLimitError, got %T", err)
		}
		if target.RetryAfter != 1500*time.Millisecond {
			t.Fatalf("RetryAfter = %v, want 1.5s", target.RetryAfter)
		}
	})
	t.Run("broker rejected", func(t *testing.T) {
		_, _, err := classify(5, []byte(errBody("BROKER_REJECTED", "")), allow01, true, false)
		var target *BrokerRejectedError
		if !errors.As(err, &target) {
			t.Fatalf("want *BrokerRejectedError, got %T", err)
		}
	})
	t.Run("network", func(t *testing.T) {
		_, _, err := classify(6, []byte(errBody("NETWORK", "")), allow01, true, false)
		var target *NetworkError
		if !errors.As(err, &target) {
			t.Fatalf("want *NetworkError, got %T", err)
		}
	})
	t.Run("outcome unknown carries reconcile hint", func(t *testing.T) {
		body := errBody("UNCONFIRMED", `"reconcile":{"order_id":"cid-1","command":"tinvest orders reconcile"}`)
		_, _, err := classify(7, []byte(body), allow01, true, false)
		var target *OutcomeUnknownError
		if !errors.As(err, &target) {
			t.Fatalf("want *OutcomeUnknownError, got %T", err)
		}
		if target.ReconcileHint.OrderID != "cid-1" || target.ReconcileHint.Command != "tinvest orders reconcile" {
			t.Fatalf("bad reconcile hint: %+v", target.ReconcileHint)
		}
	})
	t.Run("internal", func(t *testing.T) {
		_, _, err := classify(1, []byte(errBody("INTERNAL", "")), allow01, true, false)
		var target *InternalError
		if !errors.As(err, &target) {
			t.Fatalf("want *InternalError, got %T", err)
		}
	})
}

func TestClassifyExitCodeContradictsErrorCode(t *testing.T) {
	cases := map[string]struct {
		exit int
		code string
	}{
		"auth exit with usage code":     {exit: 3, code: "USAGE"},
		"usage exit with auth code":     {exit: 2, code: "AUTH"},
		"network exit with rejected":    {exit: 6, code: "BROKER_REJECTED"},
		"unconfirmed exit with network": {exit: 7, code: "NETWORK"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"ok":false,"error":{"code":"` + tc.code + `","message":"m","retryable":false},` + okMeta + `}`
			_, _, err := classify(tc.exit, []byte(body), allow01, true, false)
			var pe *ProtocolError
			if !errors.As(err, &pe) {
				t.Fatalf("want *ProtocolError for exit/code mismatch, got %T: %v", err, err)
			}
		})
	}
}

func TestClassifyUnconfirmedRequiresReconcileCommand(t *testing.T) {
	cases := map[string]string{
		"no reconcile block": `{"ok":false,"error":{"code":"UNCONFIRMED","message":"m","retryable":false},` + okMeta + `}`,
		"empty command":      `{"ok":false,"error":{"code":"UNCONFIRMED","message":"m","retryable":false,"reconcile":{"order_id":"x"}},` + okMeta + `}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := classify(7, []byte(body), allow01, true, false)
			var pe *ProtocolError
			if !errors.As(err, &pe) {
				t.Fatalf("want *ProtocolError for exit 7 without a reconcile command, got %T: %v", err, err)
			}
		})
	}
}

func TestClassifyReconcileExit1IsSuccess(t *testing.T) {
	body := `{"ok":true,"data":{"outcomes":[],"unresolved_count":2},` + okMeta + `}`
	data, _, err := classify(1, []byte(body), allow01, true, true)
	if err != nil {
		t.Fatalf("reconcile exit 1 with ok:true should be success, got %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected data payload")
	}
}

func TestClassifySuccess(t *testing.T) {
	body := `{"ok":true,"data":{"version":"0.1.0"},` + okMeta + `}`
	data, meta, err := classify(0, []byte(body), allow01, true, false)
	if err != nil {
		t.Fatalf("classify success: %v", err)
	}
	if string(data) != `{"version":"0.1.0"}` {
		t.Fatalf("data = %s", data)
	}
	if meta.SchemaVersion != "0.1" {
		t.Fatalf("meta.SchemaVersion = %q", meta.SchemaVersion)
	}
}
