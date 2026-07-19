package clock

import (
	"sync"
	"time"
)

// Simulated is a deterministic Clock whose time only moves when Advance is
// called. Timers and tickers created against it fire as Advance crosses their
// due times. It is safe for concurrent use, and all times it reports are UTC.
//
// Determinism under concurrency is the whole point (honest replay depends on
// it): a single Advance delivers at most one tick per ticker, no matter how
// many period boundaries the advance crosses. Multiple crossed boundaries
// coalesce into one tick carrying the most recent boundary. Because the number
// of ticks a concurrent consumer can observe per Advance is therefore fixed at
// 0 or 1 per ticker — independent of goroutine scheduling — an identical
// sequence of advances always produces an identical observable tick count.
// (The previous design sent every crossed boundary in a loop, so a consumer
// draining mid-loop saw a scheduling-dependent count.)
//
// Delivery still mirrors time.Ticker/time.After otherwise: each channel is
// buffered with capacity one, and a tick whose buffer is already full is
// dropped rather than blocking Advance. Consumers that must observe every tick
// drain between advances; the robot's scheduler coalesces overruns by design
// (DESIGN §6), so drop-on-full is intended.
type Simulated struct {
	mu     sync.Mutex
	now    time.Time
	events []*simEvent
}

// simEvent is a scheduled delivery. A one-shot (After) has period 0 and is
// removed after firing; a ticker has period > 0 and reschedules itself.
type simEvent struct {
	at      time.Time
	ch      chan time.Time
	period  time.Duration
	stopped bool
}

// NewSimulated returns a Simulated clock starting at the given time, normalized
// to UTC so every time it later reports (Now, After/ticker fire times) is UTC.
func NewSimulated(start time.Time) *Simulated {
	return &Simulated{now: start.UTC()}
}

// Now returns the current simulated time.
func (s *Simulated) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

// After returns a channel that receives the simulated fire time once Advance
// crosses now+d.
func (s *Simulated) After(d time.Duration) <-chan time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan time.Time, 1)
	s.events = append(s.events, &simEvent{at: s.now.Add(d), ch: ch})
	return ch
}

// NewTicker returns a Ticker that fires every d of simulated time as Advance
// crosses each boundary. It panics on a non-positive d, matching
// time.NewTicker.
func (s *Simulated) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive interval for NewTicker")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan time.Time, 1)
	ev := &simEvent{at: s.now.Add(d), ch: ch, period: d}
	s.events = append(s.events, ev)
	return &simTicker{sim: s, ev: ev}
}

// Advance moves simulated time forward by d, delivering every due timer and
// ticker. A one-shot (After) whose deadline is at or before the new time fires
// once and is removed. A ticker fires at most once regardless of how many of
// its period boundaries fall in the advanced span: the crossings coalesce into
// a single tick carrying the most recent boundary, and the ticker is rescheduled
// to the first boundary strictly after the new time. This at-most-one-per-ticker
// rule is what makes the observable tick count independent of consumer
// scheduling. Advance panics on a negative d.
func (s *Simulated) Advance(d time.Duration) {
	if d < 0 {
		panic("clock: negative advance duration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	target := s.now.Add(d)
	// Iterate in creation order. Because each event owns a distinct channel and
	// fires at most once here, the order across events cannot change any
	// channel's observable tick count, so a simple single pass is deterministic.
	for _, ev := range s.events {
		if ev.stopped || ev.at.After(target) {
			continue
		}
		fire := ev.at
		if ev.period > 0 {
			// Skip whole periods already crossed so the tick carries the most
			// recent boundary and the next fire lands strictly after target.
			skipped := target.Sub(ev.at) / ev.period
			fire = ev.at.Add(skipped * ev.period)
			ev.at = fire.Add(ev.period)
		} else {
			ev.stopped = true // one-shot: delivered once, dropped by compact
		}
		// Non-blocking send: mirror time.Ticker's drop-on-full semantics so
		// Advance never blocks on a slow consumer.
		select {
		case ev.ch <- fire:
		default:
		}
	}
	s.now = target
	s.compact()
}

// compact drops stopped ticker events so their memory is reclaimed.
func (s *Simulated) compact() {
	kept := s.events[:0]
	for _, ev := range s.events {
		if !ev.stopped {
			kept = append(kept, ev)
		}
	}
	// Clear the tail so removed events are not pinned by the backing array.
	for i := len(kept); i < len(s.events); i++ {
		s.events[i] = nil
	}
	s.events = kept
}

type simTicker struct {
	sim *Simulated
	ev  *simEvent
}

func (t *simTicker) C() <-chan time.Time { return t.ev.ch }

func (t *simTicker) Stop() {
	t.sim.mu.Lock()
	defer t.sim.mu.Unlock()
	t.ev.stopped = true
}

// Compile-time checks that both clocks satisfy the interfaces.
var (
	_ Clock  = realClock{}
	_ Clock  = (*Simulated)(nil)
	_ Ticker = (*realTicker)(nil)
	_ Ticker = (*simTicker)(nil)
)
