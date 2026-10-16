package jormungandr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ouroboric-dev/gungnir"
)

// fakeSteps plays the steps of a loop from lists: each call takes the next entry for its kind.
type fakeSteps struct {
	implement []error           // per implementer session; nil is a success
	check     map[string][]bool // per check: passes, per attempt that reaches it
	calls     []string          // what ran, in order
	inputs    []Input           // what each implementer session was given
	changes   []Change          // everything recorded
	saved     []State           // a copy of the state at every save
	saveErr   error
	ni        int
	nc        map[string]int
}

func (f *fakeSteps) Implement(_ context.Context, attempt int, in Input) error {
	f.calls = append(f.calls, fmt.Sprintf("implement %d", attempt))
	f.inputs = append(f.inputs, in)
	var err error
	if f.ni < len(f.implement) {
		err = f.implement[f.ni]
	}
	f.ni++
	return err
}

func (f *fakeSteps) Check(_ context.Context, attempt int, name string) (CheckOutcome, error) {
	f.calls = append(f.calls, fmt.Sprintf("check %d %s", attempt, name))
	if f.nc == nil {
		f.nc = map[string]int{}
	}
	i := f.nc[name]
	f.nc[name]++
	passes := f.check[name]
	ok := i >= len(passes) || passes[i]
	id := "check-" + name
	if ok {
		return CheckOutcome{Passed: true, FindingID: id, Proof: fmt.Sprintf("check %s: exit 0", name)}, nil
	}
	return CheckOutcome{FindingID: id, Finding: gungnir.Finding{ID: id, Source: name, Severity: gungnir.SeverityBlocker,
		Location: "logs/" + name + ".log:1", Claim: "check " + name + " failed", Status: gungnir.FindingOpen}}, nil
}

func (f *fakeSteps) Record(_ int, changes []Change) error {
	f.changes = append(f.changes, changes...)
	return nil
}

func (f *fakeSteps) Save(s *State) error {
	raw, _ := json.Marshal(s)
	var c State
	json.Unmarshal(raw, &c)
	f.saved = append(f.saved, c)
	return f.saveErr
}

func (f *fakeSteps) ran(call string) bool { return slices.Contains(f.calls, call) }

var cfg = Config{Attempts: 3, Checks: []string{"vet", "test"}}

func run(t *testing.T, st *State, f *fakeSteps) Outcome {
	t.Helper()
	out, err := Run(context.Background(), cfg, st, f)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return out
}

func TestWorkThatPassesEveryCheckPassesOnTheFirstAttempt(t *testing.T) {
	f := &fakeSteps{}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 1 || !st.Passed {
		t.Fatalf("Run() = %+v", out)
	}
	want := []string{"implement 1", "check 1 vet", "check 1 test"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	if len(f.inputs[0].Open) != 0 || f.inputs[0].LastFailure != "" {
		t.Errorf("the first attempt was given %+v", f.inputs[0])
	}
}

func TestAFailingCheckGoesBackToTheImplementerAndTheSlowerChecksWait(t *testing.T) {
	f := &fakeSteps{check: map[string][]bool{"test": {false, true}}}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 2 {
		t.Fatalf("Run() = %+v", out)
	}
	want := []string{"implement 1", "check 1 vet", "check 1 test", "implement 2", "check 2 vet", "check 2 test"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v\nwant    %v", f.calls, want)
	}
	if got := f.inputs[1].Open; len(got) != 1 || got[0].ID != "check-test" {
		t.Errorf("attempt 2 was given %+v; it must carry the failing check's finding", f.inputs[1])
	}
	if len(f.changes) != 2 || f.changes[0].Kind != Raised || f.changes[1].Kind != Closed || f.changes[1].Attempts != 2 {
		t.Errorf("changes = %+v; want the finding raised, then closed after 2 attempts", f.changes)
	}
}

func TestALoopThatNeverPassesEscalatesAfterItsAttempts(t *testing.T) {
	f := &fakeSteps{check: map[string][]bool{"test": {false, false, false, false}}}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Escalated || out.Reason != ReasonExhausted || out.Attempts != 3 || len(out.Open) != 1 || out.Open[0].ID != "check-test" {
		t.Fatalf("Run() = %+v", out)
	}
	if got := strings.Count(strings.Join(f.calls, "\n"), "implement"); got != 3 {
		t.Errorf("the implementer ran %d times, want 3", got)
	}
	if st.Passed {
		t.Error("an escalated loop is marked passed")
	}
}

func TestASessionThatFailedAsAnAgentUsesUpAnAttemptAndSaysWhy(t *testing.T) {
	f := &fakeSteps{implement: []error{&AgentFailure{Reason: "timed out after 20m0s"}, &AgentFailure{Reason: "exit 1"}, nil}}
	out := run(t, NewState(cfg), f)
	if out.Kind != Passed || out.Attempts != 3 {
		t.Fatalf("Run() = %+v", out)
	}
	if f.inputs[1].LastFailure != "timed out after 20m0s" || f.inputs[2].LastFailure != "exit 1" {
		t.Errorf("inputs = %+v", f.inputs)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "check") && c != "check 3 vet" && c != "check 3 test" {
			t.Errorf("%s ran, but only the third attempt produced work to check", c)
		}
	}
	// When every attempt fails that way, the loop escalates and says so.
	f = &fakeSteps{implement: []error{&AgentFailure{Reason: "a"}, &AgentFailure{Reason: "b"}, &AgentFailure{Reason: "exit 137"}}}
	out = run(t, NewState(cfg), f)
	if out.Kind != Escalated || !strings.Contains(out.Detail, "exit 137") {
		t.Errorf("Run() = %+v", out)
	}
}

func TestOtherErrorsEndTheRunOfTheLoopAsTheyAre(t *testing.T) {
	infra := &gungnir.SessionError{Kind: gungnir.FailureInfra, Err: errors.New("podman is gone")}
	for name, f := range map[string]*fakeSteps{
		"an infrastructure failure": {implement: []error{infra}},
		"exhausted retries":         {implement: []error{&ExhaustedError{Tries: 4, Last: "429"}}},
	} {
		st := NewState(cfg)
		out, err := Run(context.Background(), cfg, st, f)
		if err == nil || out.Kind != "" {
			t.Errorf("%s: Run() = %+v, %v; want the error", name, out, err)
		}
	}
	// A state that cannot be saved stops the loop before it spends anything.
	f := &fakeSteps{saveErr: errors.New("disk full")}
	if _, err := Run(context.Background(), cfg, NewState(cfg), f); err == nil || f.ran("implement 1") {
		t.Errorf("Run() = %v; a loop that can't save its state must not start a session", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, cfg, NewState(cfg), &fakeSteps{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v with a cancelled context", err)
	}
}

func TestAFinishedLoopDoesNotRunAgain(t *testing.T) {
	st := NewState(cfg)
	run(t, st, &fakeSteps{})
	f := &fakeSteps{}
	if out := run(t, st, f); out.Kind != Passed || len(f.calls) != 0 {
		t.Errorf("Run() = %+v, calls %v; a passed loop is done", out, f.calls)
	}
}
