package tinvestcli

import (
	"context"
	"errors"
	"os/exec"
	"sync"
)

// execResult is the raw outcome of one process spawn.
type execResult struct {
	stdout     []byte
	stderr     []byte
	exit       int
	stdoutFull bool // false when stdout was truncated at the cap
}

// runProcess spawns the tinvest binary with argv and captures its output. It
// never uses a shell: argv is passed directly to exec. Both output pipes are
// drained concurrently (os/exec runs a copy goroutine per non-*os.File writer),
// each bounded so a runaway child cannot exhaust memory; overflow is discarded
// but still drained so the child never blocks on a full pipe.
//
// The CLI is given its own deadline via --timeout (added by the caller); this
// function adds a hard-kill backstop at timeout+KillGrace through the context.
// A hit on that backstop, or a CLI exit reported without our being able to read
// output, is a NetworkError for a read call (retryable) but an
// OutcomeUnknownError for a mutation (spec.read == false): the child was
// spawned, so the order may have reached the broker and must be reconciled, not
// retried. Cancellation of the parent ctx is returned verbatim.
func (c *Client) runProcess(ctx context.Context, spec callSpec, argv []string) (execResult, error) {
	killCtx, cancel := context.WithTimeout(ctx, spec.timeout+c.cfg.KillGrace)
	defer cancel()

	cmd := exec.CommandContext(killCtx, c.path, argv...)
	cmd.Env = c.cfg.Env
	// SIGKILL is the CommandContext default; the CLI's own --timeout should fire
	// well before the backstop so this is rarely reached.

	stdout := &cappedBuffer{limit: c.cfg.MaxStdout}
	stderr := &cappedBuffer{limit: c.cfg.MaxStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		// The child never started, so no request left this process: a setup
		// failure, not an outcome-unknown mutation.
		return execResult{}, &ResolveError{Path: c.path, Err: err}
	}
	waitErr := cmd.Wait()

	// Parent cancellation after the child started: for a read the caller asked us
	// to stop, so return ctx.Err() as-is; for a mutation the request may already
	// have reached the broker, so its outcome is unknown and must be reconciled.
	if ctx.Err() != nil {
		if !spec.read {
			return execResult{}, c.outcomeUnknown(spec, "tinvest mutation canceled after the child started; outcome unknown")
		}
		return execResult{}, ctx.Err()
	}
	// Our own backstop fired after the child started. For a read this is a
	// transient timeout to retry; for a mutation the outcome is unknown.
	if errors.Is(killCtx.Err(), context.DeadlineExceeded) {
		if !spec.read {
			return execResult{}, c.outcomeUnknown(spec, "tinvest mutation was killed on the local deadline before a confirmed outcome")
		}
		return execResult{}, &NetworkError{
			BrokerError: BrokerError{Message: "tinvest call exceeded the local deadline"},
			Timeout:     true,
		}
	}

	res := execResult{
		stdout:     stdout.bytes(),
		stderr:     stderr.bytes(),
		stdoutFull: !stdout.truncated,
		exit:       0,
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.exit = exitErr.ExitCode()
		} else {
			// The child spawned but could not be waited on cleanly, and it was
			// neither a cancellation nor our timeout: a genuine spawn/IO failure.
			// The mutation may still have reached the broker, so its outcome is
			// unknown; a read is a plain transport failure.
			if !spec.read {
				return execResult{}, c.outcomeUnknown(spec, "tinvest mutation process failed before a confirmed outcome: "+waitErr.Error())
			}
			return execResult{}, &NetworkError{
				BrokerError: BrokerError{Message: "tinvest process failed: " + waitErr.Error()},
			}
		}
	}
	return res, nil
}

// cappedBuffer is an io.Writer that stores at most limit bytes and silently
// discards the rest, recording that truncation happened. It always reports the
// full write as consumed so os/exec's copy goroutine keeps draining the pipe.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int64
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - int64(len(b.buf))
	if room > 0 {
		take := int64(len(p))
		if take > room {
			take = room
			b.truncated = true
		}
		b.buf = append(b.buf, p[:take]...)
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf
}
