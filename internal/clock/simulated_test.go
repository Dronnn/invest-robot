package clock

import (
	"testing"
	"time"
)

var base = time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)

func recvReady(ch <-chan time.Time) (time.Time, bool) {
	select {
	case t := <-ch:
		return t, true
	default:
		return time.Time{}, false
	}
}

func TestSimulated_NowAdvances(t *testing.T) {
	c := NewSimulated(base)
	if !c.Now().Equal(base) {
		t.Fatalf("Now() = %v, want %v", c.Now(), base)
	}
	c.Advance(3 * time.Second)
	if want := base.Add(3 * time.Second); !c.Now().Equal(want) {
		t.Errorf("Now() = %v, want %v", c.Now(), want)
	}
}

func TestSimulated_AfterFires(t *testing.T) {
	c := NewSimulated(base)
	ch := c.After(5 * time.Second)

	if _, ok := recvReady(ch); ok {
		t.Fatal("After fired before its deadline")
	}
	c.Advance(4 * time.Second)
	if _, ok := recvReady(ch); ok {
		t.Fatal("After fired early at 4s")
	}
	c.Advance(1 * time.Second)
	got, ok := recvReady(ch)
	if !ok {
		t.Fatal("After did not fire at its deadline")
	}
	if want := base.Add(5 * time.Second); !got.Equal(want) {
		t.Errorf("After delivered %v, want %v", got, want)
	}
}

func TestSimulated_FiresInOrder(t *testing.T) {
	c := NewSimulated(base)
	// Created out of order; must fire by due time, not creation order.
	late := c.After(30 * time.Second)
	early := c.After(10 * time.Second)
	mid := c.After(20 * time.Second)

	c.Advance(10 * time.Second)
	if got, ok := recvReady(early); !ok || !got.Equal(base.Add(10*time.Second)) {
		t.Fatalf("early: got %v ok=%v", got, ok)
	}
	if _, ok := recvReady(mid); ok {
		t.Fatal("mid fired too early")
	}
	if _, ok := recvReady(late); ok {
		t.Fatal("late fired too early")
	}

	c.Advance(10 * time.Second)
	if got, ok := recvReady(mid); !ok || !got.Equal(base.Add(20*time.Second)) {
		t.Fatalf("mid: got %v ok=%v", got, ok)
	}
	if _, ok := recvReady(late); ok {
		t.Fatal("late fired too early")
	}

	c.Advance(10 * time.Second)
	if got, ok := recvReady(late); !ok || !got.Equal(base.Add(30*time.Second)) {
		t.Fatalf("late: got %v ok=%v", got, ok)
	}
}

func TestSimulated_TickerCounts(t *testing.T) {
	c := NewSimulated(base)
	tk := c.NewTicker(time.Second)
	defer tk.Stop()

	count := 0
	for i := 0; i < 5; i++ {
		c.Advance(time.Second)
		if got, ok := recvReady(tk.C()); ok {
			count++
			want := base.Add(time.Duration(i+1) * time.Second)
			if !got.Equal(want) {
				t.Errorf("tick %d = %v, want %v", i, got, want)
			}
		}
	}
	if count != 5 {
		t.Errorf("got %d ticks, want 5", count)
	}
}

func TestSimulated_TickerDropsWhenNotDrained(t *testing.T) {
	c := NewSimulated(base)
	tk := c.NewTicker(time.Second)
	defer tk.Stop()

	// Advance three periods without draining: only one tick is buffered, the
	// rest are dropped, matching time.Ticker.
	c.Advance(3 * time.Second)
	if _, ok := recvReady(tk.C()); !ok {
		t.Fatal("expected one buffered tick")
	}
	if _, ok := recvReady(tk.C()); ok {
		t.Fatal("expected only one buffered tick, got a second")
	}
}

func TestSimulated_TickerStop(t *testing.T) {
	c := NewSimulated(base)
	tk := c.NewTicker(time.Second)

	c.Advance(time.Second)
	if _, ok := recvReady(tk.C()); !ok {
		t.Fatal("ticker did not tick before stop")
	}
	tk.Stop()
	c.Advance(10 * time.Second)
	if _, ok := recvReady(tk.C()); ok {
		t.Error("ticker ticked after Stop")
	}
}

func TestSimulated_NegativeAdvancePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Advance(-1) did not panic")
		}
	}()
	NewSimulated(base).Advance(-time.Second)
}

func TestSimulated_NewTickerNonPositivePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewTicker(0) did not panic")
		}
	}()
	NewSimulated(base).NewTicker(0)
}

// TestSimulated_ConcurrentNowVsAdvance exercises the mutex under -race.
func TestSimulated_ConcurrentNowVsAdvance(t *testing.T) {
	c := NewSimulated(base)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_ = c.Now()
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		c.Advance(time.Millisecond)
	}
	<-done
}

func TestSimulated_NowIsUTC(t *testing.T) {
	msk := time.Date(2026, 7, 19, 15, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	c := NewSimulated(msk)
	if loc := c.Now().Location(); loc != time.UTC {
		t.Errorf("Now().Location() = %v, want UTC", loc)
	}
	if !c.Now().Equal(msk) {
		t.Errorf("Now() = %v, want the same instant as %v", c.Now(), msk)
	}
	c.Advance(time.Hour)
	if loc := c.Now().Location(); loc != time.UTC {
		t.Errorf("Now().Location() after Advance = %v, want UTC", loc)
	}
}

func TestSimulated_TickerCoalescesToOnePerAdvance(t *testing.T) {
	c := NewSimulated(base)
	tk := c.NewTicker(time.Second)
	defer tk.Stop()

	// Five period boundaries fall inside this single advance; they must
	// coalesce into exactly one tick carrying the most recent boundary.
	c.Advance(5 * time.Second)
	got, ok := recvReady(tk.C())
	if !ok {
		t.Fatal("expected one coalesced tick")
	}
	if want := base.Add(5 * time.Second); !got.Equal(want) {
		t.Errorf("coalesced tick = %v, want the most recent boundary %v", got, want)
	}
	if _, ok := recvReady(tk.C()); ok {
		t.Fatal("expected only one tick per advance, got a second")
	}
}

// TestSimulated_DeterministicTickCountUnderConcurrency is the regression for
// the replay-determinism fix: with a consumer draining concurrently (exercised
// under -race), an identical sequence of advances must produce an identical
// observable tick count every run. The consumer acks each tick so the producer
// advances in lockstep — the scheduler's drain-between-advances contract — and
// the count is asserted equal to the number of advances across 100 runs.
func TestSimulated_DeterministicTickCountUnderConcurrency(t *testing.T) {
	const advances = 50
	for iter := 0; iter < 100; iter++ {
		c := NewSimulated(base)
		tk := c.NewTicker(time.Second)

		ack := make(chan struct{})
		done := make(chan struct{})
		counts := make(chan int, 1)
		go func() {
			got := 0
			for {
				select {
				case <-tk.C():
					got++
					select {
					case ack <- struct{}{}:
					case <-done:
						counts <- got
						return
					}
				case <-done:
					counts <- got
					return
				}
			}
		}()

		for i := 0; i < advances; i++ {
			c.Advance(time.Second)
			<-ack // this advance's tick was observed before the next
		}
		close(done)
		tk.Stop()

		if got := <-counts; got != advances {
			t.Fatalf("iteration %d: observed %d ticks, want %d (tick count must be deterministic)", iter, got, advances)
		}
	}
}

// TestSimulated_ConcurrentTickerConsume runs Advance concurrently with a
// consumer draining the ticker channel, exercising the send/receive path under
// -race. Ticks may be dropped (drop-on-full semantics), so it asserts only
// that the run completes without a race or a leaked goroutine.
func TestSimulated_ConcurrentTickerConsume(t *testing.T) {
	c := NewSimulated(base)
	tk := c.NewTicker(time.Second)
	defer tk.Stop()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-tk.C():
			case <-stop:
				return
			}
		}
	}()

	for i := 0; i < 1000; i++ {
		c.Advance(time.Second)
	}
	close(stop)
	<-done
}
