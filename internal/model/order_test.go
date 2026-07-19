package model

import "testing"

func TestIntentState_IsTerminal(t *testing.T) {
	terminal := map[IntentState]bool{
		IntentFilled:   true,
		IntentCanceled: true,
		IntentRejected: true,
	}
	all := []IntentState{
		IntentNew, IntentSubmitted, IntentAcked, IntentFilled,
		IntentCanceled, IntentRejected, IntentUnknown,
	}
	for _, s := range all {
		if s.IsTerminal() != terminal[s] {
			t.Errorf("%q.IsTerminal() = %v, want %v", s, s.IsTerminal(), terminal[s])
		}
	}
}

func TestCanTransition_Table(t *testing.T) {
	// The complete allowed-edge set, written out independently of the
	// production map so a mistake in either surfaces.
	allowed := map[IntentState]map[IntentState]bool{
		IntentNew:       {IntentSubmitted: true, IntentRejected: true, IntentUnknown: true},
		IntentSubmitted: {IntentAcked: true, IntentFilled: true, IntentCanceled: true, IntentRejected: true, IntentUnknown: true},
		IntentAcked:     {IntentFilled: true, IntentCanceled: true, IntentRejected: true, IntentUnknown: true},
		IntentUnknown:   {IntentSubmitted: true, IntentAcked: true, IntentFilled: true, IntentCanceled: true, IntentRejected: true},
		IntentFilled:    {},
		IntentCanceled:  {},
		IntentRejected:  {},
	}
	all := []IntentState{
		IntentNew, IntentSubmitted, IntentAcked, IntentFilled,
		IntentCanceled, IntentRejected, IntentUnknown,
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanTransition_SameStateIsNoTransition(t *testing.T) {
	all := []IntentState{
		IntentNew, IntentSubmitted, IntentAcked, IntentFilled,
		IntentCanceled, IntentRejected, IntentUnknown,
	}
	for _, s := range all {
		if CanTransition(s, s) {
			t.Errorf("CanTransition(%q, %q) = true, want false (self is not a transition)", s, s)
		}
	}
}

func TestCanTransition_TerminalStatesAreSinks(t *testing.T) {
	all := []IntentState{
		IntentNew, IntentSubmitted, IntentAcked, IntentFilled,
		IntentCanceled, IntentRejected, IntentUnknown,
	}
	for _, from := range []IntentState{IntentFilled, IntentCanceled, IntentRejected} {
		for _, to := range all {
			if CanTransition(from, to) {
				t.Errorf("terminal %q allowed transition to %q", from, to)
			}
		}
	}
}
