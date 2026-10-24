package jormungandr

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ouroboric-dev/gungnir"
)

func finding(id, source string, sev gungnir.Severity) gungnir.Finding {
	return gungnir.Finding{ID: id, Source: source, Severity: sev, Location: "api/auth.go:42", Claim: "claim of " + id, Status: gungnir.FindingOpen}
}

// failed reports one failing finding of its source's check.
func failed(b *Book, attempt int, f gungnir.Finding) ([]Change, error) {
	return b.CheckFailed(attempt, f.Source, []gungnir.Finding{f})
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
	if ch, _ := failed(&b, 1, f); len(ch) != 1 || ch[0].Kind != Raised || ch[0].Finding.ID != "check-test" {
		t.Fatalf("first failure: %+v", ch)
	}
	f.Claim, f.Location = "newer output", "logs/b-2-test.log:1"
	if ch, _ := failed(&b, 2, f); len(ch) != 0 {
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
	failed(&b, 1, finding("check-test", "test", gungnir.SeverityBlocker))
	if ch, _ := b.CheckPassed(2, "vet", "x"); len(ch) != 0 {
		t.Errorf("a check with no finding closed something: %+v", ch)
	}
	ch, _ := b.CheckPassed(3, "test", "check test: exit 0 (logs/b-3-test.log)")
	if len(ch) != 1 || ch[0].Kind != Closed || ch[0].Attempts != 3 || ch[0].Finding.Status != gungnir.FindingFixed || ch[0].Finding.Proof == "" {
		t.Fatalf("CheckPassed() = %+v; want a close after 3 attempts with proof", ch)
	}
	if len(b.Open()) != 0 || len(b.Blocking()) != 0 {
		t.Errorf("the book still holds %v", ids(b.Open()))
	}
	if ch, _ := b.CheckPassed(4, "test", "again"); len(ch) != 0 {
		t.Errorf("a closed finding closed again: %+v", ch)
	}
}

func TestAClosedFindingThatComesBackIsReopenedAndCounted(t *testing.T) {
	var b Book
	f := finding("check-test", "test", gungnir.SeverityBlocker)
	failed(&b, 1, f)
	b.CheckPassed(2, "test", "exit 0")
	ch, _ := failed(&b, 3, f)
	if len(ch) != 1 || ch[0].Kind != Reopened || ch[0].Reopens != 1 {
		t.Fatalf("CheckFailed() = %+v; want a reopen", ch)
	}
	r := b.Records[f.ID]
	if r.Status != gungnir.FindingOpen || r.Closed != 0 || r.Reopens != 1 || r.Raised != 1 {
		t.Errorf("record = %+v", r)
	}
	ch, _ = b.CheckPassed(5, "test", "exit 0")
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
		b.put(f, 1)
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
	failed(&b, 1, finding("check-test", "test", gungnir.SeverityBlocker))
	b.CheckPassed(2, "test", "exit 0")
	failed(&b, 3, finding("check-test", "test", gungnir.SeverityBlocker))
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
	if ch, err := b.CheckPassed(2, "unit", "exit 0"); len(ch) != 0 || err != nil {
		t.Errorf("a check pass on a reviewer's finding = %+v, %v; want no change: the finding is not the check's", ch, err)
	}
	if r := b.Records["check-unit"]; r.Status != gungnir.FindingOpen || r.Source != "spec" {
		t.Errorf("record = %+v; the reviewer's finding must stay open and its own", r)
	}
	if ch, err := failed(&b, 3, finding("check-unit", "unit", gungnir.SeverityBlocker)); len(ch) != 0 || !errors.As(err, new(*ConflictError)) {
		t.Errorf("a check failure on a reviewer's finding = %+v, %v; want a conflict and no change", ch, err)
	}
	if r := b.Records["check-unit"]; r.Source != "spec" || r.Claim != "claim of check-unit" {
		t.Errorf("record = %+v; a check failure took the reviewer's finding over", r)
	}
}

// settled is what SettleDispute says of findIndexDispute when it holds.
const settled = "check test ran in attempt 2 and TestFindIndex is among the tests that passed"

func overruledBook(t *testing.T) *Book {
	t.Helper()
	var b Book
	b.Review(1, "spec", passed, report(findIndexBlocker()))
	ch, err := b.Overrule(2, findIndexDispute, settled)
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
	// The proof is the implementer's evidence and what the check's result showed, in the dispute's words: a check
	// that settles a dispute need not have passed.
	if want := "Implementer's evidence: " + findIndexDispute.Evidence + "\n" + settled; r.Proof != want {
		t.Errorf("proof = %q, want %q", r.Proof, want)
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
	failed(b, 3, finding("check-test", "test", gungnir.SeverityBlocker))
	if ch, err := b.Review(3, "spec", passed, report(same)); err != nil || len(ch) != 1 || ch[0].Kind != Reopened {
		t.Errorf("after the check failed: %+v, %v", ch, err)
	}
	// Another check failing changes nothing.
	b = overruledBook(t)
	failed(b, 3, finding("check-vet", "vet", gungnir.SeverityBlocker))
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
	failed(&b, 1, finding("check-test", "test", gungnir.SeverityBlocker))
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

// checkFindings are n failures of one check, with the ids gleipnir gives them.
func checkFindings(check string, ns ...int) []gungnir.Finding {
	var out []gungnir.Finding
	for _, n := range ns {
		f := finding(fmt.Sprintf("check-%s-%012x", check, n), check, gungnir.SeverityBlocker)
		f.Location = fmt.Sprintf("pkg/f%d_test.go:%d", n, n)
		out = append(out, f)
	}
	return out
}

func TestACheckClosesTheFailuresItNoLongerReports(t *testing.T) {
	var b Book
	ch, err := b.CheckFailed(1, "test", checkFindings("test", 1, 2, 3))
	if err != nil || len(ch) != 3 {
		t.Fatalf("first failure: %+v, %v; want three raised", ch, err)
	}
	for _, c := range ch {
		if c.Kind != Raised {
			t.Errorf("change = %+v", c)
		}
	}
	// A reviewer's finding and another check's finding are not the check's to close.
	b.put(finding("spec-1", "spec", gungnir.SeverityBlocker), 1)
	failed(&b, 1, finding("check-vet-000000000001", "vet", gungnir.SeverityBlocker))

	ch, err = b.CheckFailed(2, "test", checkFindings("test", 2))
	if err != nil || len(ch) != 2 {
		t.Fatalf("second failure: %+v, %v; want two closed", ch, err)
	}
	wantProof := "check test ran in attempt 2 and no longer reports it"
	for _, c := range ch {
		if c.Kind != Closed || c.Finding.Status != gungnir.FindingFixed || c.Finding.Proof != wantProof || c.Attempts != 2 {
			t.Errorf("change = %+v; want a close with proof %q", c, wantProof)
		}
		if err := c.Finding.Validate(); err != nil {
			t.Errorf("the closed finding is not valid: %v", err)
		}
	}
	if got := ids(ch2f(ch)); !slices.Equal(got, []string{"check-test-000000000001", "check-test-000000000003"}) {
		t.Errorf("closed = %v", got)
	}
	if got := ids(b.OpenFrom("test")); !slices.Equal(got, []string{"check-test-000000000002"}) {
		t.Errorf("open findings of test = %v; want the one still reported", got)
	}
	if len(b.OpenFrom("vet")) != 1 || len(b.OpenFrom("spec")) != 1 {
		t.Errorf("another source's findings changed: %v", ids(b.Open()))
	}

	// One that comes back reopens, and a check that fails with nothing to say is refused.
	ch, _ = b.CheckFailed(3, "test", checkFindings("test", 1, 2))
	if len(ch) != 1 || ch[0].Kind != Reopened || ch[0].Finding.ID != "check-test-000000000001" || ch[0].Reopens != 1 {
		t.Errorf("third failure: %+v; want one reopened", ch)
	}
	if ch, err := b.CheckFailed(4, "test", nil); err == nil || len(ch) != 0 || len(b.OpenFrom("test")) != 2 {
		t.Errorf("a failure with no findings = %+v, %v; want an error and no change", ch, err)
	}
	if ch, err := b.CheckFailed(4, "test", checkFindings("vet", 9)); err == nil || len(ch) != 0 {
		t.Errorf("a finding of another check = %+v, %v; want an error and no change", ch, err)
	}
	// A check's failure is open: one that says otherwise is refused, and nothing changes.
	for _, st := range []gungnir.FindingStatus{gungnir.FindingFixed, gungnir.FindingDisputed, gungnir.FindingOverruled} {
		fs := checkFindings("test", 2, 5)
		fs[1].Status, fs[1].Proof = st, "pkg/f5_test.go:5"
		if ch, err := b.CheckFailed(4, "test", fs); err == nil || len(ch) != 0 || !strings.Contains(err.Error(), string(st)) {
			t.Errorf("a %s finding = %+v, %v; want an error and no change", st, ch, err)
		}
		if _, ok := b.Records[fs[1].ID]; ok || len(b.OpenFrom("test")) != 2 {
			t.Errorf("a %s finding changed the book: %v", st, ids(b.Open()))
		}
	}
}

func ch2f(cs []Change) []gungnir.Finding {
	var out []gungnir.Finding
	for _, c := range cs {
		out = append(out, c.Finding)
	}
	slices.SortFunc(out, func(x, y gungnir.Finding) int { return strings.Compare(x.ID, y.ID) })
	return out
}

func TestAPassingCheckClosesAllItsFindings(t *testing.T) {
	var b Book
	b.CheckFailed(1, "test", checkFindings("test", 1, 2))
	b.CheckFailed(1, "vet", checkFindings("vet", 1))
	ch, err := b.CheckPassed(2, "test", "check test: exit 0")
	if err != nil || len(ch) != 2 {
		t.Fatalf("CheckPassed() = %+v, %v; want both closed", ch, err)
	}
	for _, c := range ch {
		if c.Kind != Closed || c.Finding.Status != gungnir.FindingFixed || c.Finding.Proof != "check test: exit 0" || c.Attempts != 2 {
			t.Errorf("change = %+v", c)
		}
	}
	if len(b.OpenFrom("test")) != 0 || len(b.OpenFrom("vet")) != 1 {
		t.Errorf("open = %v", ids(b.Open()))
	}
	if ch, _ := b.CheckPassed(3, "test", "again"); len(ch) != 0 {
		t.Errorf("closed findings closed again: %+v", ch)
	}
}

func TestACheckWithNoResultOpensAndClosesLikeAnyOther(t *testing.T) {
	var b Book
	f := finding("check-test-result", "test", gungnir.SeverityBlocker)
	if ch, _ := b.CheckFailed(1, "test", []gungnir.Finding{f}); len(ch) != 1 || ch[0].Kind != Raised {
		t.Fatalf("no-result failure: %+v", ch)
	}
	// The check now reports a real failure instead: the realm's fault is gone.
	ch, _ := b.CheckFailed(2, "test", checkFindings("test", 7))
	if len(ch) != 2 || b.Records["check-test-result"].Status != gungnir.FindingFixed {
		t.Errorf("CheckFailed() = %+v; want the result finding closed and the new one raised", ch)
	}
}
