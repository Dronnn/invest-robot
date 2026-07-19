package tinvestcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// methodGroup identifies a rate-limit serialization bucket. Concurrent robot
// goroutines calling into the same group are serialized so a fresh tinvest
// process never fans out parallel calls that would each carry their own
// client-side rate budget (DESIGN §4).
type methodGroup int

const (
	groupMarketData methodGroup = iota // quotes, candles, orderbook
	groupInstruments
	groupOrders
	// groupOperations is the real broker's OperationsService bucket: portfolio,
	// positions, and operations list all belong to one method group and share a
	// client-side rate budget, so they must serialize together (they are not
	// independent groups).
	groupOperations
	groupUsers // version / handshake
	groupCount
)

// Client is the sole gateway to the tinvest CLI. It is safe for concurrent use;
// calls within one method group run one at a time. Construct via Resolve, then
// Handshake before any trading call.
type Client struct {
	cfg    Config
	path   string
	sha256 string

	locks [groupCount]sync.Mutex

	mu   sync.RWMutex
	info Info // populated by a successful Handshake
}

// Info records the resolved binary and the version contract it reported.
type Info struct {
	Version       string
	Contract      string
	SchemaVersion string
	GoVersion     string
	Path          string
	SHA256        string
}

// Resolve locates the tinvest binary (cfg.Path, else $PATH), hashes it, and
// returns a Client with defaults applied. It does not run the binary — call
// Handshake for that.
func Resolve(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()

	path := cfg.Path
	switch {
	case path == "":
		found, err := exec.LookPath("tinvest")
		if err != nil {
			return nil, &ResolveError{Err: err}
		}
		path = found
	case !strings.ContainsRune(path, filepath.Separator):
		// A bare command name as an override is still resolved through $PATH.
		found, err := exec.LookPath(path)
		if err != nil {
			return nil, &ResolveError{Path: path, Err: err}
		}
		path = found
	}

	sum, err := sha256File(path)
	if err != nil {
		return nil, &ResolveError{Path: path, Err: err}
	}
	return &Client{cfg: cfg, path: path, sha256: sum}, nil
}

// Path returns the resolved binary path.
func (c *Client) Path() string { return c.path }

// SHA256 returns the hex-encoded SHA-256 of the resolved binary.
func (c *Client) SHA256() string { return c.sha256 }

// Info returns the handshake result. Version fields are empty until Handshake
// has succeeded.
func (c *Client) Info() Info {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info := c.info
	info.Path = c.path
	info.SHA256 = c.sha256
	return info
}

// Handshake runs `version -o json`, checks the reported schema_version against
// the allowlist and its internal meta/data consistency, and records the result.
// The robot refuses to start on a HandshakeError (DESIGN §4). A malformed
// version envelope surfaces as a ProtocolError.
func (c *Client) Handshake(ctx context.Context) (Info, error) {
	// The version call itself must not be rejected by the per-call schema guard —
	// checking the schema is the whole point of the handshake, and we want a
	// HandshakeError (carrying the Info) rather than a bare ProtocolError.
	raw, meta, err := c.callVersion(ctx)
	base := Info{Path: c.path, SHA256: c.sha256}
	if err != nil {
		return base, err
	}

	var vd VersionInfo
	if err := json.Unmarshal(raw, &vd); err != nil {
		return base, &ProtocolError{Reason: "malformed version data", Err: err}
	}

	info := Info{
		Version:       vd.Version,
		Contract:      vd.Contract,
		SchemaVersion: vd.SchemaVersion,
		GoVersion:     vd.Go,
		Path:          c.path,
		SHA256:        c.sha256,
	}

	if !slices.Contains(c.cfg.SchemaVersions, vd.SchemaVersion) {
		return info, &HandshakeError{
			Reason: fmt.Sprintf("version schema_version %q not in allowlist %v", vd.SchemaVersion, c.cfg.SchemaVersions),
			Info:   info,
		}
	}
	if meta.SchemaVersion != vd.SchemaVersion {
		return info, &HandshakeError{
			Reason: fmt.Sprintf("schema_version mismatch: data %q vs meta %q", vd.SchemaVersion, meta.SchemaVersion),
			Info:   info,
		}
	}
	if meta.Contract != vd.Contract {
		return info, &HandshakeError{
			Reason: fmt.Sprintf("contract mismatch: data %q vs meta %q", vd.Contract, meta.Contract),
			Info:   info,
		}
	}

	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	return info, nil
}

// callVersion runs the version command with the per-call schema guard disabled,
// returning the raw data and meta for the handshake to validate.
func (c *Client) callVersion(ctx context.Context) (json.RawMessage, wireMeta, error) {
	return c.call(ctx, callSpec{
		grp:        groupUsers,
		argv:       []string{"version"},
		timeout:    c.cfg.Timeout,
		read:       true,
		skipSchema: true,
	})
}

// callSpec describes one logical CLI call for the execution core.
type callSpec struct {
	grp                 methodGroup
	argv                []string      // subcommand path + positionals + per-command flags
	timeout             time.Duration // CLI deadline for this call
	read                bool          // read calls may retry on NetworkError/RateLimitError
	allowReconcileExit1 bool          // treat exit 1 + ok:true as success (reconcile)
	skipSchema          bool          // handshake-only: defer the schema check to the caller
}

// call runs one CLI call to completion under its method-group lock, applying the
// bounded retry policy for read calls. It returns the envelope data payload and
// meta, or one typed error.
func (c *Client) call(ctx context.Context, spec callSpec) (json.RawMessage, wireMeta, error) {
	lock := &c.locks[spec.grp]
	lock.Lock()
	defer lock.Unlock()

	argv := c.finalizeArgv(spec.argv, spec.timeout)

	maxAttempts := 1
	if spec.read {
		maxAttempts += c.cfg.Retries
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if werr := c.waitBeforeRetry(ctx, lastErr, attempt); werr != nil {
				return nil, wireMeta{}, werr
			}
		}

		res, err := c.runProcess(ctx, spec.timeout, argv)
		if err != nil {
			lastErr = err
			if spec.read && isRetryable(err) && attempt+1 < maxAttempts {
				continue
			}
			return nil, wireMeta{}, err
		}
		if !res.stdoutFull {
			return nil, wireMeta{}, &ProtocolError{Reason: "stdout truncated at the capture cap"}
		}

		data, meta, cerr := classify(res.exit, res.stdout, c.cfg.SchemaVersions, !spec.skipSchema, spec.allowReconcileExit1)
		if cerr == nil {
			return data, meta, nil
		}
		lastErr = cerr
		if spec.read && isRetryable(cerr) && attempt+1 < maxAttempts {
			continue
		}
		return nil, meta, cerr
	}
	return nil, wireMeta{}, lastErr
}

// finalizeArgv appends the global flags every invocation carries: -o json, the
// optional --profile, and --timeout matching the call's CLI deadline.
func (c *Client) finalizeArgv(argv []string, timeout time.Duration) []string {
	out := make([]string, 0, len(argv)+6)
	out = append(out, argv...)
	out = append(out, "-o", "json")
	if c.cfg.Profile != "" {
		out = append(out, "--profile", c.cfg.Profile)
	}
	out = append(out, "--timeout", timeout.String())
	return out
}

// waitBeforeRetry sleeps the appropriate backoff before a retry: the honored,
// capped retry_after for a RateLimitError; a fixed base backoff otherwise. It
// returns early with the context error if the context is canceled while waiting.
func (c *Client) waitBeforeRetry(ctx context.Context, lastErr error, attempt int) error {
	delay := c.cfg.RetryBackoff

	var rl *RateLimitError
	if errors.As(lastErr, &rl) {
		if rl.RetryAfter > 0 {
			delay = min(rl.RetryAfter, c.cfg.RetryAfterCap)
		} else {
			delay = min(c.cfg.RetryBackoff, c.cfg.RetryAfterCap)
		}
	}
	if delay <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isRetryable reports whether err is a transient class eligible for a read
// retry: a network failure (including a local timeout) or a rate limit.
func isRetryable(err error) bool {
	var net *NetworkError
	var rl *RateLimitError
	return errors.As(err, &net) || errors.As(err, &rl)
}

// sha256File returns the hex-encoded SHA-256 of the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
