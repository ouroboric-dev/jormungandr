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
	"github.com/ouroboric-dev/gungnir/schema"
)

// fakeSteps plays the steps of a loop from lists: each call takes the next entry for its kind.
type fakeSteps struct {
	implement []error                   // per implementer session; nil is a success
	answers   []schema.Implement        // per implementer session: its final answer
	check     map[string][]bool         // per check: passes, per attempt that reaches it
	results   map[string][]CheckOutcome // per check: its outcome, per attempt that reaches it; overrides check
	review    map[string][]answer       // per reviewer: its answers, in order
	calls     []string                  // what ran, in order
	inputs    []Input                   // what each implementer session was given
	changes   []Change                  // everything recorded
	saved     []State                   // a copy of the state at every save
	saveErr   error
	ni        int
	nc        map[string]int
	nr        map[string]int
}

type answer struct {
	rev schema.Review
	err error
}

func (f *fakeSteps) Implement(_ context.Context, attempt int, in Input) (schema.Implement, error) {
	f.calls = append(f.calls, fmt.Sprintf("implement %d", attempt))
	f.inputs = append(f.inputs, in)
	var err error
	if f.ni < len(f.implement) {
		err = f.implement[f.ni]
	}
	var out schema.Implement
	if f.ni < len(f.answers) {
		out = f.answers[f.ni]
	}
	f.ni++
	return out, err
}

func (f *fakeSteps) Check(_ context.Context, attempt int, name string) (CheckOutcome, error) {
	f.calls = append(f.calls, fmt.Sprintf("check %d %s", attempt, name))
	if f.nc == nil {
		f.nc = map[string]int{}
	}
	i := f.nc[name]
	f.nc[name]++
	var co CheckOutcome
	if rs := f.results[name]; i < len(rs) {
		co = rs[i]
	} else {
		passes := f.check[name]
		co.Passed = i >= len(passes) || passes[i]
	}
	co.Check = name
	switch {
	case co.Passed && co.Proof == "":
		co.Proof = fmt.Sprintf("check %s: exit 0", name)
	case !co.Passed && len(co.Findings) == 0:
		co.Findings = []gungnir.Finding{{ID: "check-" + name, Source: name, Severity: gungnir.SeverityBlocker,
			Location: "logs/" + name + ".log:1", Claim: "check " + name + " failed", Status: gungnir.FindingOpen}}
	}
	return co, nil
}

func (f *fakeSteps) Review(_ context.Context, attempt int, reviewer string, open []gungnir.Finding, passed []string) (schema.Review, error) {
	f.calls = append(f.calls, fmt.Sprintf("review %d %s", attempt, reviewer))
	if f.nr == nil {
		f.nr = map[string]int{}
	}
	i := f.nr[reviewer]
	f.nr[reviewer]++
	as := f.review[reviewer]
	if i >= len(as) {
		return schema.Review{Findings: []gungnir.Finding{}}, nil
	}
	return as[i].rev, as[i].err
}

// Record keeps the changes, and puts an overruling among the calls, so a test sees when the loop settled a dispute.
func (f *fakeSteps) Record(attempt int, changes []Change) error {
	f.changes = append(f.changes, changes...)
	for _, c := range changes {
		if c.Kind == Overruled {
			f.calls = append(f.calls, fmt.Sprintf("overruled %d %s", attempt, c.Finding.ID))
		}
	}
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

func rep(fs ...gungnir.Finding) schema.Review {
	return schema.Review{Findings: append([]gungnir.Finding{}, fs...)}
}

var cfg = Config{Attempts: 3, Checks: []string{"vet", "test"}, Reviewers: []string{"spec", "quality"}}

func run(t *testing.T, st *State, f *fakeSteps) Outcome {
	t.Helper()
	out, err := Run(context.Background(), cfg, st, f)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return out
}

func TestWorkThatPassesTheWholeLadderPassesOnTheFirstAttempt(t *testing.T) {
	f := &fakeSteps{}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 1 || !st.Passed {
		t.Fatalf("Run() = %+v", out)
	}
	want := []string{"implement 1", "check 1 vet", "check 1 test", "review 1 spec", "review 1 quality"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	if len(f.inputs[0].Open) != 0 || f.inputs[0].LastFailure != "" {
		t.Errorf("the first attempt was given %+v", f.inputs[0])
	}
}

func TestAFailingCheckGoesBackToTheImplementerAndStopsTheLadder(t *testing.T) {
	f := &fakeSteps{check: map[string][]bool{"test": {false, true}}}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 2 {
		t.Fatalf("Run() = %+v", out)
	}
	want := []string{"implement 1", "check 1 vet", "check 1 test", "implement 2", "check 2 vet", "check 2 test", "review 2 spec", "review 2 quality"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v\nwant    %v; the reviewers must not run while a check fails", f.calls, want)
	}
	if got := f.inputs[1].Open; len(got) != 1 || got[0].ID != "check-test" {
		t.Errorf("attempt 2 was given %+v; it must carry the failing check's finding", f.inputs[1])
	}
	if len(f.changes) != 2 || f.changes[0].Kind != Raised || f.changes[1].Kind != Closed || f.changes[1].Attempts != 2 {
		t.Errorf("changes = %+v; want the finding raised, then closed after 2 attempts", f.changes)
	}
}

func TestAReviewersFindingIsFixedOnlyWhenTheReviewerSaysSoWithProof(t *testing.T) {
	major := finding("spec-1", "spec", gungnir.SeverityMajor)
	f := &fakeSteps{review: map[string][]answer{
		"spec": {{rev: rep(major)}, {rev: rep(fixed(major, "api/auth.go:44 limits tries; check test passed"))}},
	}}
	out := run(t, NewState(cfg), f)
	if out.Kind != Passed || out.Attempts != 2 {
		t.Fatalf("Run() = %+v", out)
	}
	if got := f.inputs[1].Open; len(got) != 1 || got[0].ID != "spec-1" {
		t.Errorf("attempt 2 was given %+v", f.inputs[1])
	}
	if !f.ran("review 1 spec") || f.ran("review 1 quality") {
		t.Errorf("calls = %v; quality must wait until spec has no blocking finding", f.calls)
	}
	last := f.changes[len(f.changes)-1]
	if last.Kind != Closed || last.Finding.ID != "spec-1" || last.Attempts != 2 || last.Finding.Proof == "" {
		t.Errorf("last change = %+v", last)
	}
}

func TestAMinorFindingIsRecordedButDoesNotFailTheLoop(t *testing.T) {
	minor := finding("q-1", "quality", gungnir.SeverityMinor)
	f := &fakeSteps{review: map[string][]answer{"quality": {{rev: rep(minor)}}}}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 1 {
		t.Fatalf("Run() = %+v", out)
	}
	if got := st.Book.Open(); len(got) != 1 || got[0].ID != "q-1" {
		t.Errorf("the minor finding must stay in the book: %+v", got)
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

func TestAnUnreadableReviewIsAskedOnceMore(t *testing.T) {
	good := answer{rev: rep()}
	for name, bad := range map[string]answer{
		"prose":                   {err: &UnreadableError{Reviewer: "spec", Err: errors.New("not a JSON document")}},
		"a finding with no place": {err: &UnreadableError{Reviewer: "spec", Err: errors.New("/findings/0 missing location")}},
		"a failed session":        {err: &AgentFailure{Reason: "exit 1"}},
	} {
		f := &fakeSteps{review: map[string][]answer{"spec": {bad, good}}}
		st := NewState(cfg)
		out := run(t, st, f)
		if out.Kind != Passed || f.nr["spec"] != 2 || strings.Count(strings.Join(f.calls, "\n"), "implement") != 1 {
			t.Errorf("%s once: Run() = %+v, spec asked %d times; want a pass after asking again, without a new attempt", name, out, f.nr["spec"])
		}
		f = &fakeSteps{review: map[string][]answer{"spec": {bad, bad, good}}}
		st = NewState(cfg)
		out = run(t, st, f)
		if out.Kind != Escalated || out.Reason != ReasonUnreadable || st.Passed || f.nr["spec"] != 2 || f.ran("review 1 quality") {
			t.Errorf("%s twice: Run() = %+v; want an escalation: unreadable output never counts as a pass", name, out)
		}
		if !strings.Contains(out.Detail, "spec") {
			t.Errorf("%s: the detail %q doesn't name the reviewer", name, out.Detail)
		}
	}
}

func TestABlockedReviewerIsAskedOnceMoreThenEscalatesAsBlocked(t *testing.T) {
	if ReasonReviewerBlocked != "reviewer-blocked" {
		t.Errorf("ReasonReviewerBlocked = %q", ReasonReviewerBlocked)
	}
	good := answer{rev: rep()}
	blocked := answer{rev: schema.Review{Blocked: &gungnir.Blocked{Reason: "the diff is not in the workspace"}}}
	unreadable := answer{err: &UnreadableError{Reviewer: "spec", Err: errors.New("not a JSON document")}}

	f := &fakeSteps{review: map[string][]answer{"spec": {blocked, good}}}
	if out := run(t, NewState(cfg), f); out.Kind != Passed || f.nr["spec"] != 2 || strings.Count(strings.Join(f.calls, "\n"), "implement") != 1 {
		t.Errorf("blocked once: Run() = %+v, spec asked %d times; want a pass after asking again, without a new attempt", out, f.nr["spec"])
	}
	for name, c := range map[string]struct {
		answers []answer
		reason  string
		detail  string
	}{
		"blocked twice":            {[]answer{blocked, blocked, good}, ReasonReviewerBlocked, "the diff is not in the workspace"},
		"unreadable, then blocked": {[]answer{unreadable, blocked, good}, ReasonReviewerBlocked, "the diff is not in the workspace"},
		"blocked, then unreadable": {[]answer{blocked, unreadable, good}, ReasonUnreadable, "not a JSON document"},
	} {
		f := &fakeSteps{review: map[string][]answer{"spec": c.answers}}
		st := NewState(cfg)
		out := run(t, st, f)
		if out.Kind != Escalated || out.Reason != c.reason || st.Passed || f.nr["spec"] != 2 || f.ran("review 1 quality") {
			t.Errorf("%s: Run() = %+v; want an escalation as %s", name, out, c.reason)
		}
		if !strings.Contains(out.Detail, c.detail) || !strings.Contains(out.Detail, "spec") {
			t.Errorf("%s: the detail %q lacks the reviewer or %q", name, out.Detail, c.detail)
		}
	}
}

func TestAReportThatDropsAFindingIsUnreadable(t *testing.T) {
	major := finding("spec-1", "spec", gungnir.SeverityMajor)
	f := &fakeSteps{review: map[string][]answer{"spec": {{rev: rep(major)}, {rev: rep()}, {rev: rep()}}}}
	out := run(t, NewState(cfg), f)
	if out.Kind != Escalated || out.Reason != ReasonUnreadable || !strings.Contains(out.Detail, "doesn't mention") {
		t.Errorf("Run() = %+v; a report that makes a finding vanish must not pass", out)
	}
}

func TestACapOrAQuotaStopsTheLoopWhereItIsAndAResumeGoesOnFromThere(t *testing.T) {
	cap := &gungnir.CapError{Scope: gungnir.CapRun, Dimension: gungnir.CapTokens, Used: 10, Limit: 10}
	f := &fakeSteps{implement: []error{nil, cap}, check: map[string][]bool{"test": {false}}}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Capped || out.Cap != cap || out.Attempts != 2 || st.Phase != PhaseImplement || st.Attempt != 2 {
		t.Fatalf("Run() = %+v, state %+v; want a cap during attempt 2's implementer", out, st)
	}
	if got := f.saved[len(f.saved)-1]; got.Attempt != 2 || got.Phase != PhaseImplement || len(got.Book.Open()) != 1 {
		t.Errorf("the last saved state = %+v; it must hold attempt 2 and the open finding", got)
	}
	// The resume gets the saved state and does attempt 2 over: no attempt is used up.
	resumed := f.saved[len(f.saved)-1]
	g := &fakeSteps{}
	out, err := Run(context.Background(), cfg, &resumed, g)
	if err != nil || out.Kind != Passed || out.Attempts != 2 || g.calls[0] != "implement 2" {
		t.Errorf("resume: %+v, %v, calls %v", out, err, g.calls)
	}
	if len(g.inputs[0].Open) != 1 {
		t.Errorf("the resumed implementer lost the finding: %+v", g.inputs[0])
	}

	// A quota in the middle of the ladder: the ladder runs again from the top, and the implementer does not.
	q := &QuotaError{Reason: "credits used up"}
	f = &fakeSteps{review: map[string][]answer{"spec": {{err: q}}}}
	st = NewState(cfg)
	out = run(t, st, f)
	if out.Kind != Paused || out.Quota != q || st.Phase != PhaseLadder {
		t.Fatalf("Run() = %+v, state %+v", out, st)
	}
	resumed = f.saved[len(f.saved)-1]
	g = &fakeSteps{}
	out, err = Run(context.Background(), cfg, &resumed, g)
	if err != nil || out.Kind != Passed || g.ran("implement 1") || !g.ran("check 1 vet") {
		t.Errorf("resume: %+v, %v, calls %v; the implementer must not run again", out, err, g.calls)
	}
}

func TestOtherErrorsEndTheRunOfTheLoopAsTheyAre(t *testing.T) {
	infra := &gungnir.SessionError{Kind: gungnir.FailureInfra, Err: errors.New("podman is gone")}
	for name, f := range map[string]*fakeSteps{
		"the implementer":   {implement: []error{infra}},
		"a reviewer":        {review: map[string][]answer{"spec": {{err: infra}}}},
		"exhausted retries": {implement: []error{&ExhaustedError{Tries: 4, Last: "429"}}},
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

func TestAnEscalatedLoopGetsAFullSetOfAttemptsAfterTheOperatorSendsItBack(t *testing.T) {
	f := &fakeSteps{check: map[string][]bool{"test": {false, false, false, false, true}}}
	st := NewState(cfg)
	if out := run(t, st, f); out.Kind != Escalated {
		t.Fatalf("Run() = %+v", out)
	}
	st.Extend(cfg)
	if st.Limit != 6 || st.Attempt != 4 {
		t.Fatalf("after Extend: attempt %d, limit %d; want attempt 4, limit 6", st.Attempt, st.Limit)
	}
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 5 {
		t.Errorf("Run() = %+v; want a pass in attempt 5", out)
	}
	if got := f.inputs[len(f.inputs)-1].Open; len(got) != 1 || got[0].ID != "check-test" {
		t.Errorf("the findings must stay across the operator's decision: %+v", got)
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

func TestAnUnreadableAnswerIsRemembered(t *testing.T) {
	// The count of unreadable answers survives a stop between the two asks, so a resume does not give
	// a reviewer a third chance.
	cap := &gungnir.CapError{Scope: gungnir.CapDay, Dimension: gungnir.CapTurns, Used: 1, Limit: 1}
	f := &fakeSteps{review: map[string][]answer{"spec": {{err: &UnreadableError{Reviewer: "spec", Err: errors.New("prose")}}, {err: cap}}}}
	st := NewState(cfg)
	if out := run(t, st, f); out.Kind != Capped || st.Unreadable["spec"] != 1 {
		t.Fatalf("Run() = %+v, unreadable %v", out, st.Unreadable)
	}
	resumed := f.saved[len(f.saved)-1]
	g := &fakeSteps{review: map[string][]answer{"spec": {{err: &UnreadableError{Reviewer: "spec", Err: errors.New("more prose")}}}}}
	out, err := Run(context.Background(), cfg, &resumed, g)
	if err != nil || out.Kind != Escalated || out.Reason != ReasonUnreadable {
		t.Errorf("Run() = %+v, %v; the second unreadable answer, across a stop, must escalate", out, err)
	}
}

func TestABlockingFindingFromASourceThatNoLongerRunsKeepsTheLoopFromPassing(t *testing.T) {
	// A state carried over from a ladder with another reviewer in it: nobody can close its finding now,
	// and the work must not pass over it.
	st := NewState(cfg)
	failed(&st.Book, 1, finding("ghost-1", "ghost", gungnir.SeverityMajor))
	f := &fakeSteps{}
	out := run(t, st, f)
	if out.Kind == Passed || st.Passed {
		t.Errorf("Run() = %+v; a loop passed with a blocking finding open", out)
	}
}

func TestABlockerReportedAgainAsMinorDoesNotLetTheLoopPass(t *testing.T) {
	blocker := finding("spec-1", "spec", gungnir.SeverityBlocker)
	lowered := blocker
	lowered.Severity = gungnir.SeverityMinor
	f := &fakeSteps{review: map[string][]answer{
		"spec": {{rev: rep(blocker)}, {rev: rep(lowered)}, {rev: rep(lowered)}},
	}}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind == Passed || st.Passed {
		t.Fatalf("Run() = %+v; a blocker lowered to minor with no proof must not pass", out)
	}
}

func TestABlockedImplementerEscalatesAtOnce(t *testing.T) {
	f := &fakeSteps{answers: []schema.Implement{{
		Blocked:  &gungnir.Blocked{Reason: "the contract is missing from the spec"},
		Disputes: []gungnir.Dispute{findIndexDispute}, // ignored: the attempt ends
	}}}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Escalated || out.Reason != ReasonBlocked || out.Attempts != 1 || out.Detail != "the contract is missing from the spec" {
		t.Fatalf("Run() = %+v", out)
	}
	if ReasonBlocked != "implementer-blocked" {
		t.Errorf("ReasonBlocked = %q", ReasonBlocked)
	}
	if want := []string{"implement 1"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v: no check, dispute or review runs for a blocked attempt", f.calls, want)
	}
	if st.Passed || len(st.Disputes) != 0 {
		t.Errorf("state = %+v", st)
	}
	if st.Attempt != 2 || st.Phase != PhaseImplement || st.Failure != "you answered that you were blocked: the contract is missing from the spec" {
		t.Errorf("saved state: attempt %d, phase %q, failure %q; want the next attempt, told why", st.Attempt, st.Phase, st.Failure)
	}
	if last := f.saved[len(f.saved)-1]; last.Attempt != 2 || last.Phase != PhaseImplement || !strings.Contains(last.Failure, "the contract is missing from the spec") {
		t.Errorf("last save = attempt %d, phase %q, failure %q", last.Attempt, last.Phase, last.Failure)
	}
	// The operator sends the work back: the next attempt is told why the last one stopped.
	st.Extend(cfg)
	g := &fakeSteps{}
	if out := run(t, st, g); out.Kind != Passed || out.Attempts != 2 {
		t.Fatalf("after the operator sent it back: Run() = %+v", out)
	}
	if got := g.inputs[0].LastFailure; got != "you answered that you were blocked: the contract is missing from the spec" {
		t.Errorf("attempt 2's LastFailure = %q; want the blocked reason", got)
	}
}
