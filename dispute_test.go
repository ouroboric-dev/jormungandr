package jormungandr

import (
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

func holds() map[string]DisputeOutcome {
	return map[string]DisputeOutcome{"spec-1": {Verdict: Holds, Proof: "check test ran in attempt 2 and TestFindIndex does not fail"}}
}

func TestADisputeThatHoldsOverrulesTheFindingAndTheLoopPasses(t *testing.T) {
	b := findIndexBlocker()
	f := &fakeSteps{
		answers:  []schema.Implement{{}, disputeDoc(findIndexDispute)},
		verdicts: holds(),
		// The reviewer repeats the claim in attempt 2 even though it was told nothing is open: it must not reopen.
		review: map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(b)}}},
	}
	st := NewState(cfg)
	out := run(t, st, f)
	if out.Kind != Passed || out.Attempts != 2 || !st.Passed {
		t.Fatalf("Run() = %+v; a held dispute must end as passed, not attempts-exhausted", out)
	}
	want := []string{"implement 2", "check 2 vet", "check 2 test", "dispute 2 spec-1", "review 2 spec", "review 2 quality"}
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
	for _, w := range []string{findIndexDispute.Evidence, "TestFindIndex"} {
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
		review:  map[string][]answer{"spec": {{rev: rep(b)}, {rev: rep(b)}, {rev: rep(fixed(b, "web/list.js:12 is right; check test passed"))}}},
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
	for name, dd := range map[string]gungnir.Dispute{
		"a check the loop does not run": {ID: "spec-1", Check: "lint", Test: "T", Evidence: "see web/list.js:12"},
		"a check Odin cannot settle":    findIndexDispute,
	} {
		f := &fakeSteps{
			answers:  []schema.Implement{{}, disputeDoc(dd)},
			verdicts: map[string]DisputeOutcome{"spec-1": {Verdict: Unsettleable, Reason: "no check covers it"}},
			review:   map[string][]answer{"spec": {{rev: rep(b)}}},
		}
		st := NewState(cfg)
		out := run(t, st, f)
		if out.Kind != Escalated || out.Reason != ReasonDisputed || out.Attempts != 2 {
			t.Fatalf("%s: Run() = %+v", name, out)
		}
		if f.ran("review 2 spec") {
			t.Errorf("%s: the reviewers ran after the escalation", name)
		}
		for _, w := range []string{"findIndex is not a JavaScript method", "web/list.js:12", dd.Evidence} {
			if !strings.Contains(out.Detail, w) {
				t.Errorf("%s: detail lacks %q:\n%s", name, w, out.Detail)
			}
		}
		if len(out.Open) != 1 || st.Passed {
			t.Errorf("%s: open %v passed %v", name, ids(out.Open), st.Passed)
		}
		// The operator sends it back: the dispute is not decided again, and the next attempt starts.
		if st.Phase != PhaseImplement || len(st.Disputes) != 0 {
			t.Errorf("%s: state %+v", name, st)
		}
		st.Extend(cfg)
		g := &fakeSteps{review: map[string][]answer{"spec": {{rev: rep(b)}}}}
		if out := run(t, st, g); g.ran("dispute 3 spec-1") || out.Reason == ReasonDisputed {
			t.Errorf("%s: after the operator's ruling Run() = %+v, calls %v", name, out, g.calls)
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
	for _, c := range f.calls {
		if strings.HasPrefix(c, "dispute") {
			t.Errorf("%s ran for an answer with no disputes", c)
		}
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
		answers:  []schema.Implement{{}, disputeDoc(ds...)},
		verdicts: map[string]DisputeOutcome{"no-such": {Verdict: Holds, Proof: "p"}, "check-vet": {Verdict: Holds, Proof: "p"}},
		check:    map[string][]bool{"vet": {true, false}},
		review:   map[string][]answer{"spec": {{rev: rep(b)}}},
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
	for _, c := range f.calls {
		if strings.HasPrefix(c, "dispute") {
			t.Errorf("%s ran, but neither dispute can be decided", c)
		}
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
	for _, w := range []string{"`disputes`", "check", "is not among the tests it reports as failed"} {
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

func TestADisputeHoldsWhenTheTestNoLongerFails(t *testing.T) {
	d := findIndexDispute // check "test", test "TestFindIndex"
	cases := []struct {
		name string
		ran  map[string]CheckOutcome
		want Verdict
	}{
		{"the check passed", map[string]CheckOutcome{"test": {Passed: true, Proof: "check test: exit 0"}}, Holds},
		{"the check failed on other tests", map[string]CheckOutcome{"test": {Failed: []string{"TestOther"}}}, Holds},
		{"the check still fails the test", map[string]CheckOutcome{"test": {Failed: []string{"TestOther", "TestFindIndex"}}}, DoesNotHold},
		{"the check failed and named no test", map[string]CheckOutcome{"test": {}}, DoesNotHold},
		{"the check did not run", map[string]CheckOutcome{"vet": {Passed: true}}, DoesNotHold},
		{"no check ran", nil, DoesNotHold},
	}
	for _, c := range cases {
		got := SettleDispute(2, d, c.ran)
		if got.Verdict != c.want {
			t.Errorf("%s: verdict %q, want %q (%+v)", c.name, got.Verdict, c.want, got)
		}
		if c.want == Holds && got.Proof == "" || c.want == DoesNotHold && got.Reason == "" {
			t.Errorf("%s: %+v lacks its proof or reason", c.name, got)
		}
	}
	// A passing check's output need not name the test: the typed result names failures, not passes.
	if got := SettleDispute(2, d, map[string]CheckOutcome{"test": {Passed: true, Proof: "exit 0"}}); got.Verdict != Holds || !strings.Contains(got.Proof, "exit 0") {
		t.Errorf("a pass with a silent output = %+v", got)
	}
}
