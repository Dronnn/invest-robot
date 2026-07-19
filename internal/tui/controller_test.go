package tui

import "testing"

func TestStubControllerToggles(t *testing.T) {
	c := NewStubController("PAPER")
	if got := c.Status().State; got != EngineRunning {
		t.Fatalf("initial state = %q, want running", got)
	}
	if got := c.Status().Mode; got != "PAPER" {
		t.Fatalf("mode = %q, want PAPER", got)
	}

	c.Pause()
	if c.Pauses != 1 || c.Status().State != EnginePaused {
		t.Fatalf("after Pause: pauses=%d state=%q", c.Pauses, c.Status().State)
	}
	c.Resume()
	if c.Resumes != 1 || c.Status().State != EngineRunning {
		t.Fatalf("after Resume: resumes=%d state=%q", c.Resumes, c.Status().State)
	}
	c.KillSwitch()
	if c.Kills != 1 || c.Status().State != EngineHalted {
		t.Fatalf("after KillSwitch: kills=%d state=%q", c.Kills, c.Status().State)
	}
}

func TestStubControllerDefaultMode(t *testing.T) {
	if got := NewStubController("").Status().Mode; got != ModePaper {
		t.Fatalf("empty mode defaulted to %q, want PAPER", got)
	}
}

func TestStubControllerSetStatus(t *testing.T) {
	c := NewStubController("REAL")
	c.SetStatus(EngineStatus{Mode: "REAL", LastCycle: CycleSummary{ID: 7, Engine: "rules"}})
	s := c.Status()
	if s.State != EngineRunning { // empty State keeps prior
		t.Fatalf("state clobbered to %q", s.State)
	}
	if s.LastCycle.ID != 7 {
		t.Fatalf("last cycle id = %d, want 7", s.LastCycle.ID)
	}
}

func TestStubCancelRequester(t *testing.T) {
	c := &StubCancelRequester{}
	if err := c.RequestCancel("abc"); err != nil {
		t.Fatalf("RequestCancel err = %v", err)
	}
	if got := c.Requests(); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("requests = %v", got)
	}
}
