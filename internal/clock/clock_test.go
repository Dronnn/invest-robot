package clock

import (
	"testing"
	"time"
)

func TestReal_Now(t *testing.T) {
	c := Real()
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, outside [%v, %v]", got, before, after)
	}
}

func TestReal_NowIsUTC(t *testing.T) {
	if loc := Real().Now().Location(); loc != time.UTC {
		t.Errorf("Real().Now().Location() = %v, want UTC", loc)
	}
}

func TestReal_After(t *testing.T) {
	c := Real()
	select {
	case <-c.After(10 * time.Millisecond):
	case <-time.After(2 * time.Second):
		t.Fatal("After did not fire within the timeout")
	}
}

func TestReal_Ticker(t *testing.T) {
	c := Real()
	tk := c.NewTicker(5 * time.Millisecond)
	defer tk.Stop()
	for i := 0; i < 3; i++ {
		select {
		case <-tk.C():
		case <-time.After(2 * time.Second):
			t.Fatalf("ticker did not deliver tick %d in time", i)
		}
	}
}
