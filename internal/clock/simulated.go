package clock

import (
	"sync"
	"time"
)

// Simulated is a deterministic Clock whose time only moves when Advance is
// called. Timers and tickers created against it fire, in ascending fire-time
// order, as Advance crosses their due times. It is safe for concurrent use.
//
// Delivery mirrors time.Ticker/time.After: each timer or ticker channel is
// buffered with capacity one, and a fire whose buffer is already full is
// dropped rather than blocking Advance. Consumers that must not miss ticks
// therefore drain between advances; the robot's scheduler coalesces overruns
// by design, so drop-on-full is the intended behavior.
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

// NewSimulated returns a Simulated clock starting at the given time.
func NewSimulated(start time.Time) *Simulated {
	return &Simulated{now: start}
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

// Advance moves simulated time forward by d, firing every due timer and ticker
// in ascending fire-time order (ties break by creation order). It panics on a
// negative d.
func (s *Simulated) Advance(d time.Duration) {
	if d < 0 {
		panic("clock: negative advance duration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	target := s.now.Add(d)
	for {
		idx := -1
		var best time.Time
		for i, ev := range s.events {
			if ev.stopped || ev.at.After(target) {
				continue
			}
			if idx == -1 || ev.at.Before(best) {
				idx, best = i, ev.at
			}
		}
		if idx == -1 {
			s.now = target
			break
		}

		ev := s.events[idx]
		s.now = ev.at
		fire := ev.at
		if ev.period > 0 {
			ev.at = ev.at.Add(ev.period)
		} else {
			s.events = append(s.events[:idx], s.events[idx+1:]...)
		}
		// Non-blocking send: mirror time.Ticker's drop-on-full semantics so
		// Advance never blocks on a slow consumer.
		select {
		case ev.ch <- fire:
		default:
		}
	}
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
