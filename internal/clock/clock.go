// Package clock abstracts time so the robot never calls time.Now() directly
// below the orchestration layer. A Real clock wraps the time package for
// production; a Simulated clock lets tests and backtests advance time
// deterministically. This is what makes replay honest: the same inputs and the
// same clock advances produce the same decisions.
package clock

import "time"

// Ticker delivers ticks on a channel until stopped, mirroring time.Ticker.
type Ticker interface {
	// C returns the channel on which ticks are delivered.
	C() <-chan time.Time
	// Stop halts the ticker. It does not close the channel.
	Stop()
}

// Clock is the injectable time source. Now reads the current time; After and
// NewTicker create timers relative to it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) Ticker
}

// Real returns a Clock backed by the time package.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTicker(d time.Duration) Ticker       { return &realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()               { r.t.Stop() }
