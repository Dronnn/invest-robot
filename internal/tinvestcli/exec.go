package tinvestcli

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
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
// output, is surfaced by the caller as a NetworkError. Cancellation of the
// parent ctx is returned verbatim.
func (c *Client) runProcess(ctx context.Context, timeout time.Duration, argv []string) (execResult, error) {
	killCtx, cancel := context.WithTimeout(ctx, timeout+c.cfg.KillGrace)
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
		return execResult{}, &ResolveError{Path: c.path, Err: err}
	}
	waitErr := cmd.Wait()

	// Parent cancellation takes precedence and is returned as-is (not a broker
	// error): the caller asked us to stop.
	if ctx.Err() != nil {
		return execResult{}, ctx.Err()
	}
	// Our own backstop fired: treat as a transient timeout so reads may retry.
	if errors.Is(killCtx.Err(), context.DeadlineExceeded) {
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
			// The process could not be waited on cleanly and it was neither a
			// cancellation nor our timeout: a genuine spawn/IO failure.
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
