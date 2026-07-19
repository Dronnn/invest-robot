package tinvestcli

import "time"

// Config configures the tinvest adapter. It is owned by this package (not
// internal/config) so tinvestcli depends only on the standard library and
// internal/model; the app wiring maps its own config section onto this struct.
// The zero value is not usable — construct via Resolve, which fills defaults.
type Config struct {
	// Path is an explicit tinvest binary path. Empty means resolve "tinvest"
	// from $PATH (DESIGN §2: config override → $PATH).
	Path string

	// Profile is passed as --profile on every command when non-empty.
	Profile string

	// Timeout is the per-call deadline given to the CLI (--timeout) for ordinary
	// commands. Default 30s.
	Timeout time.Duration

	// CandlesTimeout is the longer per-call deadline for candles downloads.
	// Default 60s.
	CandlesTimeout time.Duration

	// KillGrace is added on top of the CLI timeout before the adapter hard-kills
	// the process via context, so the CLI can emit its own timeout envelope
	// first. Default 5s.
	KillGrace time.Duration

	// Retries is the number of extra attempts (beyond the first) for read calls
	// that fail with a NetworkError or RateLimitError. Mutations never retry.
	// Zero means the default of 2; a negative value disables retries entirely.
	Retries int

	// RetryBackoff is the base pause before a NetworkError retry. Default 100ms.
	RetryBackoff time.Duration

	// RetryAfterCap bounds how long a RateLimitError's retry_after_ms is
	// honored. Default 30s.
	RetryAfterCap time.Duration

	// MaxStdout / MaxStderr bound captured output. Beyond the cap the extra is
	// discarded (and flagged as truncation → ProtocolError). Defaults 32 MiB /
	// 1 MiB.
	MaxStdout int64
	MaxStderr int64

	// SchemaVersions is the exact envelope schema_version allowlist. Default
	// {"0.1"}.
	SchemaVersions []string

	// Stream supervision tuning (StreamMarketdata). Zero fields take defaults.
	//
	// StreamQueueSize is the bounded event-channel capacity. StreamLineLimit
	// bounds one NDJSON line; a longer line is a ProtocolError. StreamMaxFastRestarts
	// is the circuit breaker: that many consecutive restarts each shorter than
	// StreamMinHealthyRun trips it into a terminal StreamDownError. Restart backoff
	// is jittered exponential between StreamBaseBackoff and StreamMaxBackoff.
	StreamQueueSize       int
	StreamLineLimit       int
	StreamMaxFastRestarts int
	StreamMinHealthyRun   time.Duration
	StreamBaseBackoff     time.Duration
	StreamMaxBackoff      time.Duration

	// Env is the environment passed to the child process. Nil means inherit the
	// current process environment. tinvest resolves its own token from here; the
	// robot never injects one.
	Env []string
}

const (
	defaultTimeout        = 30 * time.Second
	defaultCandlesTimeout = 60 * time.Second
	defaultKillGrace      = 5 * time.Second
	defaultRetries        = 2
	defaultRetryBackoff   = 100 * time.Millisecond
	defaultRetryAfterCap  = 30 * time.Second
	defaultMaxStdout      = 32 << 20 // 32 MiB
	defaultMaxStderr      = 1 << 20  // 1 MiB

	defaultStreamQueueSize       = 256
	defaultStreamLineLimit       = 4 << 20 // 4 MiB
	defaultStreamMaxFastRestarts = 5
	defaultStreamMinHealthyRun   = 10 * time.Second
	defaultStreamBaseBackoff     = 500 * time.Millisecond
	defaultStreamMaxBackoff      = 30 * time.Second
)

// withDefaults returns a copy of cfg with zero fields replaced by their
// defaults.
func (cfg Config) withDefaults() Config {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.CandlesTimeout <= 0 {
		cfg.CandlesTimeout = defaultCandlesTimeout
	}
	if cfg.KillGrace <= 0 {
		cfg.KillGrace = defaultKillGrace
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	} else if cfg.Retries == 0 {
		cfg.Retries = defaultRetries
	}
	if cfg.RetryBackoff < 0 {
		cfg.RetryBackoff = 0
	} else if cfg.RetryBackoff == 0 {
		cfg.RetryBackoff = defaultRetryBackoff
	}
	if cfg.RetryAfterCap <= 0 {
		cfg.RetryAfterCap = defaultRetryAfterCap
	}
	if cfg.MaxStdout <= 0 {
		cfg.MaxStdout = defaultMaxStdout
	}
	if cfg.MaxStderr <= 0 {
		cfg.MaxStderr = defaultMaxStderr
	}
	if len(cfg.SchemaVersions) == 0 {
		cfg.SchemaVersions = []string{"0.1"}
	}
	if cfg.StreamQueueSize <= 0 {
		cfg.StreamQueueSize = defaultStreamQueueSize
	}
	if cfg.StreamLineLimit <= 0 {
		cfg.StreamLineLimit = defaultStreamLineLimit
	}
	if cfg.StreamMaxFastRestarts <= 0 {
		cfg.StreamMaxFastRestarts = defaultStreamMaxFastRestarts
	}
	if cfg.StreamMinHealthyRun <= 0 {
		cfg.StreamMinHealthyRun = defaultStreamMinHealthyRun
	}
	if cfg.StreamBaseBackoff <= 0 {
		cfg.StreamBaseBackoff = defaultStreamBaseBackoff
	}
	if cfg.StreamMaxBackoff <= 0 {
		cfg.StreamMaxBackoff = defaultStreamMaxBackoff
	}
	return cfg
}
