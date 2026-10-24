package jormungandr

import (
	"testing"

	"github.com/ouroboric-dev/gungnir"
)

func TestALoopThatPassedIsReopenedWhenSomethingDownstreamRefusesItsWork(t *testing.T) {
	st := NewState(cfg)
	nit := gungnir.Finding{ID: "q-1", Source: "quality", Severity: gungnir.SeverityMinor, Location: "a.go:1", Claim: "a nit", Status: gungnir.FindingOpen}
	f := &fakeSteps{review: map[string][]answer{"quality": {{rev: rep(nit)}}}}
	if out := run(t, st, f); out.Kind != Passed || out.Attempts != 1 {
		t.Fatalf("the first run = %+v", out)
	}
	st.Reopen(cfg)
	if st.Passed || st.Attempt != 2 || st.Phase != PhaseImplement || st.Limit != 4 {
		t.Fatalf("after Reopen: passed %v, attempt %d, phase %s, limit %d; want a new attempt 2 with attempts up to 4", st.Passed, st.Attempt, st.Phase, st.Limit)
	}
	again := &fakeSteps{review: map[string][]answer{"quality": {{rev: rep(nit)}}}} // a reviewer must mention what it still holds open
	out := run(t, st, again)
	if out.Kind != Passed || out.Attempts != 2 {
		t.Errorf("the reopened loop = %+v; want a pass in attempt 2", out)
	}
	if !again.ran("implement 2") || again.ran("implement 1") {
		t.Errorf("calls = %v; the implementer runs again, once", again.calls)
	}
	if len(again.inputs[0].Open) != 1 || again.inputs[0].Open[0].ID != "q-1" {
		t.Errorf("the findings must stay across a reopening: %+v", again.inputs[0].Open)
	}
}

func TestReopeningALoopThatIsNotFinishedGivesItAFullSetOfAttempts(t *testing.T) {
	f := &fakeSteps{check: map[string][]bool{"test": {false, false, false}}}
	st := NewState(cfg)
	if out := run(t, st, f); out.Kind != Escalated {
		t.Fatalf("Run() = %+v", out)
	}
	st.Reopen(cfg)
	if st.Limit != 6 || st.Attempt != 4 || st.Passed {
		t.Errorf("attempt %d, limit %d, passed %v; an escalated loop is extended, as an operator's rejection does", st.Attempt, st.Limit, st.Passed)
	}
}
