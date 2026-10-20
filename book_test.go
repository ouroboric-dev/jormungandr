package jormungandr

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ouroboric-dev/gungnir"
)

func finding(id, source string, sev gungnir.Severity) gungnir.Finding {
	return gungnir.Finding{ID: id, Source: source, Severity: sev, Location: "api/auth.go:42", Claim: "claim of " + id, Status: gungnir.FindingOpen}
}

func ids(fs []gungnir.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func TestACheckThatKeepsFailingKeepsOneFinding(t *testing.T) {
	var b Book
	f := finding("check-test", "test", gungnir.SeverityBlocker)
	if ch, _ := b.CheckFailed(1, f); len(ch) != 1 || ch[0].Kind != Raised || ch[0].Finding.ID != "check-test" {
		t.Fatalf("first failure: %+v", ch)
	}
	f.Claim, f.Location = "newer output", "logs/b-2-test.log:1"
	if ch, _ := b.CheckFailed(2, f); len(ch) != 0 {
		t.Errorf("a failure that goes on is not a new finding: %+v", ch)
	}
	if got := b.Open(); len(got) != 1 || got[0].Claim != "newer output" || got[0].Location != "logs/b-2-test.log:1" {
		t.Errorf("Open() = %+v; the finding must follow the latest output", got)
	}
	if b.Records["check-test"].Raised != 1 {
		t.Errorf("the finding was raised at attempt %d, want 1", b.Records["check-test"].Raised)
	}
}

func TestACheckClosesItsFindingOnlyWhenItPasses(t *testing.T) {
	var b Book
	b.CheckFailed(1, finding("check-test", "test", gungnir.SeverityBlocker))
	if ch, _ := b.CheckPassed(2, "vet", "check-vet", "x"); len(ch) != 0 {
		t.Errorf("a check with no finding closed something: %+v", ch)
	}
	ch, _ := b.CheckPassed(3, "test", "check-test", "check test: exit 0 (logs/b-3-test.log)")
	if len(ch) != 1 || ch[0].Kind != Closed || ch[0].Attempts != 3 || ch[0].Finding.Status != gungnir.FindingFixed || ch[0].Finding.Proof == "" {
		t.Fatalf("CheckPassed() = %+v; want a close after 3 attempts with proof", ch)
	}
	if len(b.Open()) != 0 || len(b.Blocking()) != 0 {
		t.Errorf("the book still holds %v", ids(b.Open()))
	}
	if ch, _ := b.CheckPassed(4, "test", "check-test", "again"); len(ch) != 0 {
		t.Errorf("a closed finding closed again: %+v", ch)
	}
}

func TestAClosedFindingThatComesBackIsReopenedAndCounted(t *testing.T) {
	var b Book
	f := finding("check-test", "test", gungnir.SeverityBlocker)
	b.CheckFailed(1, f)
	b.CheckPassed(2, "test", f.ID, "exit 0")
	ch, _ := b.CheckFailed(3, f)
	if len(ch) != 1 || ch[0].Kind != Reopened || ch[0].Reopens != 1 {
		t.Fatalf("CheckFailed() = %+v; want a reopen", ch)
	}
	r := b.Records[f.ID]
	if r.Status != gungnir.FindingOpen || r.Closed != 0 || r.Reopens != 1 || r.Raised != 1 {
		t.Errorf("record = %+v", r)
	}
	ch, _ = b.CheckPassed(5, "test", f.ID, "exit 0")
	if len(ch) != 1 || ch[0].Attempts != 5 || ch[0].Reopens != 1 {
		t.Errorf("CheckPassed() = %+v; want it closed after 5 attempts, having reopened once", ch)
	}
}

func TestOpenFindingsComeWorstFirstInAStableOrder(t *testing.T) {
	var b Book
	for _, f := range []gungnir.Finding{
		finding("z-minor", "r", gungnir.SeverityMinor), finding("b-major", "r", gungnir.SeverityMajor),
		finding("a-major", "s", gungnir.SeverityMajor), finding("m-blocker", "r", gungnir.SeverityBlocker),
	} {
		b.CheckFailed(1, f)
	}
	if got := ids(b.Open()); !slices.Equal(got, []string{"m-blocker", "a-major", "b-major", "z-minor"}) {
		t.Errorf("Open() = %v", got)
	}
	if got := ids(b.Blocking()); !slices.Equal(got, []string{"m-blocker", "a-major", "b-major"}) {
		t.Errorf("Blocking() = %v; a minor finding must not block", got)
	}
	if got := ids(b.OpenFrom("r")); !slices.Equal(got, []string{"m-blocker", "b-major", "z-minor"}) {
		t.Errorf("OpenFrom(r) = %v", got)
	}
}

func TestABookSurvivesARoundTripThroughRunJSON(t *testing.T) {
	var b Book
	b.CheckFailed(1, finding("check-test", "test", gungnir.SeverityBlocker))
	b.CheckPassed(2, "test", "check-test", "exit 0")
	b.CheckFailed(3, finding("check-test", "test", gungnir.SeverityBlocker))
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var back Book
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	r := back.Records["check-test"]
	if r == nil || r.Reopens != 1 || r.Raised != 1 || r.Status != gungnir.FindingOpen {
		t.Errorf("after the round trip: %+v from %s", r, raw)
	}
	if raw2, _ := json.Marshal(back); string(raw2) != string(raw) {
		t.Errorf("the encoding is not stable:\n%s\n%s", raw, raw2)
	}
}

func TestACheckNeverTouchesAFindingAnotherSourceRaised(t *testing.T) {
	var b Book
	b.put(finding("check-unit", "spec", gungnir.SeverityBlocker), 1) // a state from before the reviewer ids were reserved
	if ch, err := b.CheckPassed(2, "unit", "check-unit", "exit 0"); len(ch) != 0 || !errors.As(err, new(*ConflictError)) {
		t.Errorf("a check pass on a reviewer's finding = %+v, %v; want a conflict and no change", ch, err)
	}
	if r := b.Records["check-unit"]; r.Status != gungnir.FindingOpen || r.Source != "spec" {
		t.Errorf("record = %+v; the reviewer's finding must stay open and its own", r)
	}
	if ch, err := b.CheckFailed(3, finding("check-unit", "unit", gungnir.SeverityBlocker)); len(ch) != 0 || !errors.As(err, new(*ConflictError)) {
		t.Errorf("a check failure on a reviewer's finding = %+v, %v; want a conflict and no change", ch, err)
	}
	if r := b.Records["check-unit"]; r.Source != "spec" || r.Claim != "claim of check-unit" {
		t.Errorf("record = %+v; a check failure took the reviewer's finding over", r)
	}
}

func overruledBook(t *testing.T) *Book {
	t.Helper()
	var b Book
	b.Review(1, "spec", passed, report(findIndexBlocker()))
	ch, err := b.Overrule(2, findIndexDispute, "check test passed in attempt 2")
	if err != nil || len(ch) != 1 || ch[0].Kind != Overruled {
		t.Fatalf("Overrule() = %+v, %v", ch, err)
	}
	return &b
}

func TestAnOverruledFindingNoLongerBlocksAndIsNotAskedOfItsReviewer(t *testing.T) {
	b := overruledBook(t)
	if len(b.Open()) != 0 || len(b.Blocking()) != 0 || len(b.OpenFrom("spec")) != 0 {
		t.Errorf("open %v", ids(b.Open()))
	}
	r := b.Records["spec-1"]
	if r.Status != gungnir.FindingOverruled || r.OverruledBy != "test" || r.Closed != 2 || r.Attempts() != 2 {
		t.Errorf("record = %+v", r)
	}
	// An empty report is fine: an overruled finding is not "still open and not mentioned".
	if ch, err := b.Review(3, "spec", passed, report()); err != nil || len(ch) != 0 {
		t.Errorf("Review() = %+v, %v", ch, err)
	}
}

func TestAReviewerRepeatingAnOverruledFindingDoesNotReopenItUnlessThereIsProof(t *testing.T) {
	same := findIndexBlocker()
	b := overruledBook(t)
	if ch, err := b.Review(3, "spec", passed, report(same)); err != nil || len(ch) != 0 || len(b.Blocking()) != 0 {
		t.Errorf("a repeat at the same place: %+v, %v, blocking %v", ch, err, ids(b.Blocking()))
	}
	moved := same
	moved.Location = "web/list.js:40"
	ch, err := b.Review(3, "spec", passed, report(moved))
	if err != nil || len(ch) != 1 || ch[0].Kind != Reopened || ch[0].Reopens != 1 || len(b.Blocking()) != 1 {
		t.Fatalf("a repeat at another place: %+v, %v", ch, err)
	}
	if r := b.Records["spec-1"]; r.OverruledBy != "" || r.Closed != 0 || r.Status != gungnir.FindingOpen {
		t.Errorf("record = %+v", r)
	}

	// The check that overruled it fails in a later attempt: the repeat reopens it.
	b = overruledBook(t)
	b.CheckFailed(3, finding("check-test", "test", gungnir.SeverityBlocker))
	if ch, err := b.Review(3, "spec", passed, report(same)); err != nil || len(ch) != 1 || ch[0].Kind != Reopened {
		t.Errorf("after the check failed: %+v, %v", ch, err)
	}
	// Another check failing changes nothing.
	b = overruledBook(t)
	b.CheckFailed(3, finding("check-vet", "vet", gungnir.SeverityBlocker))
	if ch, err := b.Review(3, "spec", passed, report(same)); err != nil || len(ch) != 0 {
		t.Errorf("after another check failed: %+v, %v", ch, err)
	}
}

func TestAReviewerCannotOverruleOrCloseAnOverruledFinding(t *testing.T) {
	b := overruledBook(t)
	f := findIndexBlocker()
	f.Status, f.Proof = gungnir.FindingOverruled, "web/list.js:12"
	if _, err := b.Review(3, "spec", passed, report(f)); err == nil {
		t.Error("a reviewer overruled a finding")
	}
	if ch, err := b.Review(3, "spec", passed, report(fixed(findIndexBlocker(), "web/list.js:12; check test passed"))); err != nil || len(ch) != 0 {
		t.Errorf("a fixed report of an overruled finding: %+v, %v", ch, err)
	}
}

func TestOnlyAnOpenFindingOfAReviewerCanBeOverruled(t *testing.T) {
	var b Book
	b.CheckFailed(1, finding("check-test", "test", gungnir.SeverityBlocker))
	b.Review(1, "spec", passed, report(findIndexBlocker()))
	for _, id := range []string{"check-test", "nope"} {
		d := findIndexDispute
		d.ID = id
		if ch, err := b.Overrule(2, d, "p"); err == nil || len(ch) != 0 {
			t.Errorf("Overrule(%s) = %+v, %v", id, ch, err)
		}
	}
	b.Overrule(2, findIndexDispute, "p")
	if _, err := b.Overrule(3, findIndexDispute, "p"); err == nil {
		t.Error("an overruled finding was overruled again")
	}
}
