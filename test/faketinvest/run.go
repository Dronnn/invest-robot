package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// envLookup abstracts environment access so tests can inject values without
// touching the real process environment.
type envLookup func(string) string

// run is the testable entry point. It parses a tinvest-style argv, loads the
// scenario, produces the response, and returns the process exit code. It never
// calls os.Exit, so tests can drive it in-process.
func run(ctx context.Context, argv []string, env envLookup, stdout, stderr io.Writer) int {
	p := parseArgs(argv)

	s, err := loadScenarioFor(env, p)
	if err != nil {
		fmt.Fprintf(stderr, "faketinvest: %v\n", err)
		return exitInternal
	}

	isStream := strings.HasPrefix(p.command, "stream")

	// Reject a malformed argv exactly as real cobra/pflag parsing would: a
	// value flag with no value, or a flag that doesn't exist for this command
	// (finding: unknown flags and missing values must be exit 2, not silently
	// accepted).
	if p.flagErr != "" {
		return writeUsageFailure(stdout, s, isStream, &errorBody{Code: "USAGE", Message: p.flagErr})
	}
	if body := validateFlags(argv, p.command); body != nil {
		return writeUsageFailure(stdout, s, isStream, body)
	}

	// Validate -o exactly as the real CLI does for unary commands; streams
	// ignore output mode and always emit NDJSON.
	if !isStream {
		out := firstNonEmpty(p.flag("-o", "--output"), env("TINVEST_OUTPUT"))
		if out != "" && out != "json" && out != "table" {
			body := &errorBody{Code: "USAGE", Message: fmt.Sprintf("invalid output format %q (want json or table)", out)}
			_ = writeEnvelope(stdout, envelope{OK: false, Error: body, Meta: s.metaFor(false)})
			return exitUsage
		}
	}

	switch {
	case p.command == "version":
		return s.emitVersion(stdout)
	case p.command == "stream marketdata":
		return runMarketDataStream(ctx, s, p, stdout)
	case isStream:
		_ = writeFrame(stdout, s.errorFrame("USAGE", fmt.Sprintf("unsupported stream command %q", p.command), s.AccountID))
		return exitUsage
	}

	// Unary broker command: count the invocation (for failure injection), apply
	// any injected failure, otherwise build the real response.
	call := bumpCount(stateDir(env, s.dir), p.command)
	if s.DefaultLatencyMS > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(s.DefaultLatencyMS) * time.Millisecond):
		}
	}

	if fail := matchFailure(s.Fail, p.command, call); fail != nil {
		body, exit := fail.toError(p.flag("--order-id"))
		_ = writeEnvelope(stdout, envelope{OK: false, Error: body, Meta: s.metaFor(true)})
		return exit
	}

	res := buildUnary(s, p)
	if res.errBody != nil {
		_ = writeEnvelope(stdout, envelope{OK: false, Error: res.errBody, Meta: s.metaFor(true)})
		return res.exit
	}
	_ = writeEnvelope(stdout, envelope{OK: true, Data: res.data, Meta: s.metaFor(true)})
	return res.exit
}

// writeUsageFailure emits a parse-time failure (unknown flag, missing value)
// through the right channel for the command shape: an NDJSON error frame for a
// stream command, an envelope for a unary one. Both mirror cobra's behavior of
// failing before any command-specific RunE logic ever executes.
func writeUsageFailure(w io.Writer, s *scenario, isStream bool, body *errorBody) int {
	if isStream {
		_ = writeFrame(w, s.errorFrame(body.Code, body.Message, s.AccountID))
	} else {
		_ = writeEnvelope(w, envelope{OK: false, Error: body, Meta: s.metaFor(false)})
	}
	return exitForCode(body.Code)
}

// versionData mirrors the real `version` data payload field order
// (version, contract, schema_version, go).
type versionData struct {
	Version       string `json:"version"`
	Contract      string `json:"contract"`
	SchemaVersion string `json:"schema_version"`
	Go            string `json:"go"`
}

// emitVersion writes the version envelope. Like the real CLI, version carries no
// account or tracking id and reports zero elapsed time.
func (s *scenario) emitVersion(w io.Writer) int {
	data, _ := json.Marshal(versionData{
		Version:       s.Version,
		Contract:      s.Contract,
		SchemaVersion: s.SchemaVersion,
		Go:            s.GoVersion,
	})
	_ = writeEnvelope(w, envelope{OK: true, Data: data, Meta: s.metaFor(false)})
	return exitOK
}

// metaFor builds envelope metadata. Account-scoped commands carry the scenario
// account and tracking id and the configured latency as elapsed_ms; version
// carries none of those.
func (s *scenario) metaFor(account bool) meta {
	m := meta{Contract: s.Contract, SchemaVersion: s.SchemaVersion}
	if account {
		m.AccountID = s.AccountID
		m.TrackingID = s.TrackingID
		m.ElapsedMS = s.DefaultLatencyMS
	}
	return m
}

// loadScenarioFor resolves the scenario directory from the --scenario flag or
// the FAKETINVEST_SCENARIO env var. With neither set it returns a default
// scenario so unauthenticated commands (version) work with zero configuration.
func loadScenarioFor(env envLookup, p parsedArgs) (*scenario, error) {
	dir := firstNonEmpty(p.flag("--scenario"), env("FAKETINVEST_SCENARIO"))
	if dir == "" {
		return defaultScenario(), nil
	}
	return loadScenario(dir)
}

// defaultScenario is the zero-configuration scenario: contract constants and an
// empty universe.
func defaultScenario() *scenario {
	return &scenario{
		SchemaVersion: defaultSchemaVersion,
		Contract:      defaultContract,
		Version:       defaultVersion,
		GoVersion:     defaultGoVersion,
		Orders:        ordersSpec{OrderIDPrefix: "ord-", Place: placeOutcomeSpec{Lifecycle: "EXECUTION_REPORT_STATUS_FILL"}},
		index:         map[string]*instrumentSpec{},
	}
}
