package jormungandr

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ouroboric-dev/gungnir"
	"github.com/ouroboric-dev/gungnir/schema"
)

func disputeDoc(ds ...gungnir.Dispute) schema.Implement {
	return schema.Implement{Summary: "I checked this with a test.", Disputes: ds}
}

var findIndexDispute = gungnir.Dispute{ID: "spec-1", Check: "test", Test: "TestFindIndex", Evidence: "node -e showed [1,2].findIndex(x => x == 2) returns 1; web/list.js:12"}

func findIndexBlocker() gungnir.Finding {
	f := finding("spec-1", "spec", gungnir.SeverityBlocker)
	f.Location, f.Claim = "web/list.js:12", "findIndex is not a JavaScript method"
	return f
}

// passesFindIndex is check test's outcomes: attempt 1 passes with no test listed, attempt 2 lists TestFindIndex
// among the tests that passed.
func passesFindIndex() map[string][]CheckOutcome {
	return map[string][]CheckOutcome{"test": {{Passed: true}, {Passed: true, PassedTests: []string{"TestOther", "TestFindIndex"}}}}
}

func TestADisputeThatHoldsOverrulesTheFindingAndTheLoopPasses(t *testing.T) {
	b := findIndexBlocker()
	f := &fakeSteps{
		answers: []schema.Implement{{}, disputeDoc(findIndexDispute)},
		results: passesFindIndex(),
		// The reviewer repeats the claim in attempt 2 even though it was told nothing is open: it must not reopen.
		review: map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(b)}}},
	}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 2 || !st.Passed {
		t.Fatalf("Run() = %+v; a held dispute must end as passed, not attempts-exhausted", out)
	}
	want := []string{"implement 2", "check 2 vet", "check 2 test", "overruled 2 spec-1", "review 2 spec", "review 2 quality"}
	if got := f.calls[slices.Index(f.calls, "implement 2"):]; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v; the dispute is decided after the checks and before the reviewers", got, want)
	}
	var ov *Change
	for i := range f.changes {
		if f.changes[i].Kind == Overruled {
			ov = &f.changes[i]
		}
	}
	if ov == nil || ov.Finding.ID != "spec-1" || ov.Finding.Status != gungnir.FindingOverruled || ov.Check != "test" || ov.Attempts != 2 {
		t.Fatalf("overruled change = %+v", ov)
	}
	for _, w := range []string{findIndexDispute.Evidence, "check test ran in attempt 2 and TestFindIndex is among the tests that passed"} {
		if !strings.Contains(ov.Finding.Proof, w) {
			t.Errorf("proof lacks %q: %q", w, ov.Finding.Proof)
		}
	}
	if err := ov.Finding.Validate(); err != nil {
		t.Errorf("the overruled finding is not valid: %v", err)
	}
	for _, c := range f.changes {
		if c.Kind == Reopened {
			t.Errorf("the reviewer's repeat reopened the finding: %+v", c)
		}
	}
	if len(st.Book.Open()) != 0 || len(st.Book.Blocking()) != 0 {
		t.Errorf("open = %v", ids(st.Book.Open()))
	}
}

func TestADisputeThatDoesNotHoldLeavesTheFindingOpenAndTheNextAttemptIsToldWhy(t *testing.T) {
	b := findIndexBlocker()
	f := &fakeSteps{
		answers: []schema.Implement{{}, disputeDoc(findIndexDispute), {}},
		// Attempt 2: check test still fails TestFindIndex, so the ladder stops before the reviewers.
		results: map[string][]CheckOutcome{"test": {{Passed: true}, {Failed: []string{"TestFindIndex"}}}},
		review:  map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(fixed(b, "web/list.js:12 is right; check test passed"))}}},
	}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 3 {
		t.Fatalf("Run() = %+v", out)
	}
	for _, c := range f.changes {
		if c.Kind == Overruled {
			t.Errorf("an unproven dispute overruled the finding: %+v", c)
		}
	}
	if len(f.inputs[1].Rejected) != 0 {
		t.Errorf("attempt 2 was told of a rejection before it disputed: %+v", f.inputs[1].Rejected)
	}
	got := f.inputs[2].Rejected
	if len(got) != 1 || got[0].ID != "spec-1" || !strings.Contains(got[0].Reason, "still fails TestFindIndex") {
		t.Fatalf("attempt 3 input rejected = %+v", got)
	}
	text := FeedbackWith(f.inputs[2].Open, got, "", "")
	for _, w := range []string{"## Your disputes that did not hold", "spec-1", "check test", "TestFindIndex", "still fails TestFindIndex", "stays open"} {
		if !strings.Contains(text, w) {
			t.Errorf("feedback lacks %q:\n%s", w, text)
		}
	}
}

func TestADisputeThatNeverHoldsEscalatesAsDisputedWhenTheAttemptsRunOut(t *testing.T) {
	b := findIndexBlocker()
	d := disputeDoc(findIndexDispute)
	f := &fakeSteps{answers: []schema.Implement{d, d, d}, review: map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(b)}, {rev: rep(b)}}}}
	out := run(t, NewState(cfg), f)
	if out.Kind != Escalated || out.Reason != ReasonDisputed || out.Attempts != 3 {
		t.Fatalf("Run() = %+v", out)
	}
	for _, w := range []string{"findIndex is not a JavaScript method", "web/list.js:12", findIndexDispute.Evidence, "spec-1"} {
		if !strings.Contains(out.Detail, w) {
			t.Errorf("detail lacks %q:\n%s", w, out.Detail)
		}
	}
	// Disputed only in an early attempt, not in the last: the plain reason stands.
	f = &fakeSteps{answers: []schema.Implement{d, {}, {}}, review: map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(b)}, {rev: rep(b)}}}}
	if out := run(t, NewState(cfg), f); out.Kind != Escalated || out.Reason != ReasonExhausted {
		t.Errorf("Run() = %+v; want attempts-exhausted when the last attempt disputed nothing", out)
	}
}

func TestADisputeNoCheckCanSettleEscalatesAtOnceWithBothSides(t *testing.T) {
	b := findIndexBlocker()
	dd := gungnir.Dispute{ID: "spec-1", Check: "lint", Test: "T", Evidence: "see web/list.js:12"} // a check the loop does not run
	f := &fakeSteps{
		answers: []schema.Implement{{}, disputeDoc(dd)},
		results: map[string][]CheckOutcome{"lint": {{Passed: true, PassedTests: []string{"T"}}}}, // never asked: lint is not a check of the loop
		review:  map[string][]answer{"spec": {{rev: rep(b)}}},
	}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Escalated || out.Reason != ReasonDisputed || out.Attempts != 2 {
		t.Fatalf("Run() = %+v", out)
	}
	if f.ran("review 2 spec") || f.ran("check 2 lint") {
		t.Errorf("calls %v: the reviewers ran after the escalation, or a check outside the loop ran", f.calls)
	}
	for _, w := range []string{"findIndex is not a JavaScript method", "web/list.js:12", dd.Evidence, `check "lint" is not one of this loop's checks`} {
		if !strings.Contains(out.Detail, w) {
			t.Errorf("detail lacks %q:\n%s", w, out.Detail)
		}
	}
	if len(out.Open) != 1 || st.Passed {
		t.Errorf("open %v passed %v", ids(out.Open), st.Passed)
	}
	// The operator sends it back: the dispute is not decided again, and the next attempt starts.
	if st.Phase != PhaseImplement || len(st.Disputes) != 0 {
		t.Errorf("state %+v", st)
	}
	st.Extend(cfg)
	g := &fakeSteps{review: map[string][]answer{"spec": {{rev: rep(b)}}}}
	if out := run(t, st, g); out.Reason == ReasonDisputed {
		t.Errorf("after the operator's ruling Run() = %+v, calls %v", out, g.calls)
	}
}

func TestTheLoopSettlesADisputeOnTheChecksOfItsAttempt(t *testing.T) {
	ghost := findIndexDispute
	ghost.Test = "TestDoesNotExist"
	cases := []struct {
		name    string
		d       gungnir.Dispute
		results map[string][]CheckOutcome // attempt 1, then attempt 2
		held    bool
		why     string // in the rejection's reason
	}{
		{"the check lists the test as passed", findIndexDispute, passesFindIndex(), true, ""},
		{"a passing check and a test that does not exist", ghost, passesFindIndex(), false, "TestDoesNotExist is not among the tests that passed"},
		{"a passing check that lists no passing test", findIndexDispute, map[string][]CheckOutcome{"test": {{Passed: true}, {Passed: true}}}, false, "lists no passing tests"},
		{"a failing check that lists other tests", findIndexDispute, map[string][]CheckOutcome{"test": {{Passed: true}, {Failed: []string{"TestOther"}, PassedTests: []string{"TestThird"}}}}, false, "TestFindIndex is not among the tests that passed"},
		{"a failing check with no failing test", findIndexDispute, map[string][]CheckOutcome{"test": {{Passed: true}, {PassedTests: []string{"TestFindIndex"}}}}, false, "names no failing test"},
		// vet fails first, so the ladder stops and test does not run in attempt 2; its pass in attempt 1 proves nothing.
		{"a check that did not run in the attempt", findIndexDispute, map[string][]CheckOutcome{
			"vet":  {{Passed: true}, {}},
			"test": {{Passed: true, PassedTests: []string{"TestFindIndex"}}},
		}, false, "did not run in attempt 2"},
	}
	for _, c := range cases {
		b := findIndexBlocker()
		f := &fakeSteps{
			answers: []schema.Implement{{}, disputeDoc(c.d)},
			results: c.results,
			review:  map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(b)}, {rev: rep(b)}}},
		}
		two := Config{Attempts: 2, Checks: cfg.Checks, Reviewers: cfg.Reviewers}
		st := NewState(two)
		if _, err := Run(context.Background(), two, st, f); err != nil {
			t.Fatalf("%s: Run() = %v", c.name, err)
		}
		r := st.Book.Records["spec-1"]
		if held := r.Status == gungnir.FindingOverruled; held != c.held {
			t.Errorf("%s: overruled %v, want %v; calls %v", c.name, held, c.held, f.calls)
		}
		if c.held {
			continue
		}
		if len(st.Rejected) != 1 || !strings.Contains(st.Rejected[0].Reason, c.why) {
			t.Errorf("%s: rejected %+v, want a reason with %q", c.name, st.Rejected, c.why)
		}
	}
}

func TestAnAnswerWithNoDisputesDocumentChangesNothing(t *testing.T) {
	b := findIndexBlocker()
	f := &fakeSteps{answers: []schema.Implement{{}, {Summary: "I fixed the loop bound in web/list.js and added a test."}}, review: map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(fixed(b, "web/list.js:12; check test passed"))}}}}
	out := run(t, NewState(cfg), f)
	if out.Kind != Passed {
		t.Fatalf("Run() = %+v", out)
	}
	for _, c := range f.changes {
		if c.Kind == DisputeIgnored {
			t.Errorf("a plain answer was recorded as an ignored dispute: %+v", c)
		}
	}
}

func TestADisputeAboutAFindingThatCannotBeDisputedIsRecordedAndIgnored(t *testing.T) {
	b := findIndexBlocker()
	ds := []gungnir.Dispute{
		{ID: "no-such", Check: "test", Test: "T", Evidence: "e"},
		{ID: "check-vet", Check: "test", Test: "T", Evidence: "e"},
	}
	f := &fakeSteps{
		answers: []schema.Implement{{}, disputeDoc(ds...)},
		// test would settle both disputes if it ran: it lists T among the tests that passed.
		results: map[string][]CheckOutcome{"test": {{Passed: true, PassedTests: []string{"T"}}, {Passed: true, PassedTests: []string{"T"}}}},
		check:   map[string][]bool{"vet": {true, false}},
		review:  map[string][]answer{"spec": {{rev: rep(b)}}},
	}
	st := NewState(cfg)
	// Attempt 1: spec raises the blocker. Attempt 2: vet fails, so check-vet is open.
	run(t, st, f)
	var reasons []string
	for _, c := range f.changes {
		if c.Kind == DisputeIgnored {
			reasons = append(reasons, c.Reason)
		}
	}
	if len(reasons) != 2 || !strings.Contains(reasons[0], "no-such") || !strings.Contains(reasons[1], "check-vet") {
		t.Fatalf("ignored reasons = %q", reasons)
	}
	if r := st.Book.Records["check-vet"]; r == nil || r.Status != gungnir.FindingOpen && r.Status != gungnir.FindingFixed {
		t.Errorf("check-vet = %+v", r)
	}
	for _, c := range f.changes {
		if c.Kind == Overruled {
			t.Errorf("overruled: %+v", c)
		}
	}
}

func TestTheDisputeInstructionsComeOnlyWithOpenFindings(t *testing.T) {
	base := "Do the task."
	if got := WithDisputeFormat(base, nil, []string{"vet", "test"}); got != base {
		t.Errorf("no open findings: %q", got)
	}
	got := WithDisputeFormat(base, []gungnir.Finding{findIndexBlocker()}, []string{"vet", "test"})
	if !strings.HasPrefix(got, base) || !strings.Contains(got, DisputeFormat) || !strings.Contains(got, "vet, test") {
		t.Errorf("open findings:\n%s", got)
	}
	for _, w := range []string{"`disputes`", "check", "The dispute holds when the named check runs in the attempt and its result lists the named test among the tests that passed."} {
		if !strings.Contains(DisputeFormat, w) {
			t.Errorf("DisputeFormat lacks %q:\n%s", w, DisputeFormat)
		}
	}
	if got := WithDisputeFormat(base, []gungnir.Finding{findIndexBlocker()}, nil); !strings.Contains(got, "no checks") {
		t.Errorf("no checks:\n%s", got)
	}
}

func TestTheReviewerIsToldARepeatedBlockerAfterADisputeNeedsProof(t *testing.T) {
	for _, text := range []string{ReviewRequest("spec", nil, nil, true)} {
		for _, w := range []string{"disputed", "failing check", "different location"} {
			if !strings.Contains(text, w) {
				t.Errorf("lacks %q:\n%s", w, text)
			}
		}
	}
}

func TestADisputeHoldsOnlyWhenTheCheckListsTheTestAsPassed(t *testing.T) {
	d := findIndexDispute // check "test", test "TestFindIndex"
	ghost := d
	ghost.Test = "TestDoesNotExist"
	cases := []struct {
		name string
		d    gungnir.Dispute
		ran  map[string]CheckOutcome
		want Verdict
		why  string // in the proof or the reason
	}{
		{"the check passed and lists the test", d, map[string]CheckOutcome{"test": {Passed: true, Proof: "exit 0", PassedTests: []string{"TestOther", "TestFindIndex"}}}, Holds, "TestFindIndex is among the tests that passed"},
		{"the check failed on other tests and lists the test as passed", d, map[string]CheckOutcome{"test": {Failed: []string{"TestOther"}, PassedTests: []string{"TestFindIndex"}}}, Holds, "TestFindIndex is among the tests that passed"},
		{"the check passed and the test does not exist", ghost, map[string]CheckOutcome{"test": {Passed: true, Proof: "exit 0", PassedTests: []string{"TestFindIndex"}}}, DoesNotHold, "TestDoesNotExist is not among the tests that passed"},
		{"the check passed and lists no passing test", d, map[string]CheckOutcome{"test": {Passed: true, Proof: "exit 0"}}, DoesNotHold, "lists no passing tests"},
		{"the check failed on other tests and does not list the test", d, map[string]CheckOutcome{"test": {Failed: []string{"TestOther"}, PassedTests: []string{"TestThird"}}}, DoesNotHold, "TestFindIndex is not among the tests that passed"},
		{"the check failed on other tests and lists no passing test", d, map[string]CheckOutcome{"test": {Failed: []string{"TestOther"}}}, DoesNotHold, "lists no passing tests"},
		{"the check still fails the test", d, map[string]CheckOutcome{"test": {Failed: []string{"TestOther", "TestFindIndex"}}}, DoesNotHold, "still fails TestFindIndex"},
		{"the check failed and named no test", d, map[string]CheckOutcome{"test": {PassedTests: []string{"TestFindIndex"}}}, DoesNotHold, "names no failing test"},
		{"the check did not run", d, map[string]CheckOutcome{"vet": {Passed: true, PassedTests: []string{"TestFindIndex"}}}, DoesNotHold, "did not run in attempt 2"},
		{"no check ran", d, nil, DoesNotHold, "did not run in attempt 2"},
	}
	for _, c := range cases {
		got := SettleDispute(2, c.d, c.ran)
		if got.Verdict != c.want {
			t.Errorf("%s: verdict %q, want %q (%+v)", c.name, got.Verdict, c.want, got)
		}
		if text := got.Proof + got.Reason; !strings.Contains(text, c.why) {
			t.Errorf("%s: %+v does not say %q", c.name, got, c.why)
		}
		if c.want == Holds && (got.Proof == "" || got.Reason != "") || c.want == DoesNotHold && (got.Reason == "" || got.Proof != "") {
			t.Errorf("%s: %+v; a held dispute has a proof, one that does not hold a reason", c.name, got)
		}
	}
}
