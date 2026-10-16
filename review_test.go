package jormungandr

import (
	"errors"
	"strings"
	"testing"

	"github.com/ouroboric-dev/gungnir"
)

func report(fs ...gungnir.Finding) gungnir.Report { return gungnir.Report{Findings: fs} }

func fixed(f gungnir.Finding, proof string) gungnir.Finding {
	f.Status, f.Proof = gungnir.FindingFixed, proof
	return f
}

var passed = []string{"vet", "test"}

func TestAReviewerRaisesFindingsAndEmptyIsAPass(t *testing.T) {
	var b Book
	ch, err := b.Review(1, "spec", passed, report(finding("spec-1", "spec", gungnir.SeverityMajor), finding("spec-2", "spec", gungnir.SeverityMinor)))
	if err != nil || len(ch) != 2 || ch[0].Kind != Raised {
		t.Fatalf("Review() = %+v, %v", ch, err)
	}
	if got := ids(b.Blocking()); len(got) != 1 || got[0] != "spec-1" {
		t.Errorf("Blocking() = %v", got)
	}
	var empty Book
	if ch, err := empty.Review(1, "spec", passed, report()); err != nil || len(ch) != 0 {
		t.Errorf("an empty report: %+v, %v; it says the reviewer found nothing", ch, err)
	}
}

func TestAFindingClosesOnlyWithProofFromItsSource(t *testing.T) {
	var b Book
	f := finding("spec-1", "spec", gungnir.SeverityMajor)
	b.Review(1, "spec", passed, report(f))
	ch, err := b.Review(2, "spec", passed, report(fixed(f, "api/auth.go:44 now limits tries; check test passed")))
	if err != nil || len(ch) != 1 || ch[0].Kind != Closed || ch[0].Attempts != 2 || ch[0].Finding.Proof == "" {
		t.Fatalf("Review() = %+v, %v; want a close after 2 attempts", ch, err)
	}
	if len(b.Open()) != 0 {
		t.Errorf("still open: %v", ids(b.Open()))
	}
}

func TestAReviewerReportThatBreaksARuleChangesNothing(t *testing.T) {
	f := finding("spec-1", "spec", gungnir.SeverityMajor)
	cases := map[string]struct {
		report gungnir.Report
		want   string
	}{
		"a finding dropped without a word":             {report(), "doesn't mention it"},
		"a close without a place":                      {report(fixed(f, "check test passed, it looks right")), "without proof"},
		"a close without a check":                      {report(fixed(f, "api/auth.go:44 limits tries")), "without proof"},
		"a close naming a check that didn't pass":      {report(fixed(f, "api/auth.go:44; check lint passed")), "without proof"},
		"a close naming a check by a part of its name": {report(fixed(f, "api/auth.go:44; the tests passed")), "without proof"},
		"a finding of another source":                  {report(f, finding("q-1", "quality", gungnir.SeverityMinor)), "says its source"},
		"a finding born closed":                        {report(f, fixed(finding("spec-9", "spec", gungnir.SeverityMinor), "api/a.go:1; check vet passed")), "was not open"},
	}
	for name, c := range cases {
		var b Book
		b.Review(1, "spec", passed, report(f))
		before := len(b.Open())
		ch, err := b.Review(2, "spec", passed, c.report)
		var re *ReportError
		if !errors.As(err, &re) || !strings.Contains(err.Error(), c.want) || ch != nil {
			t.Errorf("%s: Review() = %+v, %v; want a ReportError containing %q", name, ch, err, c.want)
		}
		if len(b.Open()) != before || b.Records["spec-1"].Status != gungnir.FindingOpen {
			t.Errorf("%s: a refused report changed the book", name)
		}
	}
}

func TestAReviewerCannotTouchAnotherSourcesFinding(t *testing.T) {
	var b Book
	b.CheckFailed(1, finding("check-test", "test", gungnir.SeverityBlocker))
	_, err := b.Review(1, "spec", passed, report(fixed(finding("check-test", "spec", gungnir.SeverityBlocker), "api/a.go:1; check test passed")))
	if err == nil || !strings.Contains(err.Error(), "was not open") && !strings.Contains(err.Error(), "belongs to") {
		t.Errorf("Review() = %v; a reviewer closed a check's finding", err)
	}
	_, err = b.Review(1, "spec", passed, report(finding("check-test", "spec", gungnir.SeverityBlocker)))
	if err == nil || !strings.Contains(err.Error(), "belongs to test") {
		t.Errorf("Review() = %v; a reviewer took over a check's finding", err)
	}
}

func TestAReviewerThatHasNoChecksToCiteNeedsOnlyAPlace(t *testing.T) {
	var b Book
	f := finding("spec-1", "spec", gungnir.SeverityMajor)
	b.Review(1, "spec", nil, report(f))
	if _, err := b.Review(2, "spec", nil, report(fixed(f, "api/auth.go:44 limits tries"))); err != nil {
		t.Errorf("Review() = %v; with no checks in the loop, a place is the whole proof", err)
	}
}

func TestADisputedFindingStaysOpenAndAClosedOneReopens(t *testing.T) {
	var b Book
	f := finding("spec-1", "spec", gungnir.SeverityMajor)
	b.Review(1, "spec", passed, report(f))
	disputed := f
	disputed.Status = gungnir.FindingDisputed
	if ch, err := b.Review(2, "spec", passed, report(disputed)); err != nil || len(ch) != 0 || len(b.Blocking()) != 1 {
		t.Errorf("a disputed finding: %+v, %v, blocking %v; it is unresolved", ch, err, ids(b.Blocking()))
	}
	b.Review(3, "spec", passed, report(fixed(f, "api/auth.go:44; check vet passed")))
	ch, err := b.Review(4, "spec", passed, report(f))
	if err != nil || len(ch) != 1 || ch[0].Kind != Reopened || ch[0].Reopens != 1 {
		t.Fatalf("Review() = %+v, %v; want a reopen", ch, err)
	}
	if r := b.Records["spec-1"]; r.Raised != 1 || r.Closed != 0 || r.Status != gungnir.FindingOpen {
		t.Errorf("record = %+v", r)
	}
}

func TestAReviewerMayRewordAnOpenFindingButNotItsIdentity(t *testing.T) {
	var b Book
	f := finding("spec-1", "spec", gungnir.SeverityMajor)
	b.Review(1, "spec", passed, report(f))
	moved := f
	moved.Location, moved.Claim, moved.Severity = "api/auth.go:60", "the throttle is per-IP, not per-PIN", gungnir.SeverityBlocker
	if ch, err := b.Review(2, "spec", passed, report(moved)); err != nil || len(ch) != 0 {
		t.Fatalf("Review() = %+v, %v", ch, err)
	}
	if got := b.Open()[0]; got.Location != "api/auth.go:60" || got.Severity != gungnir.SeverityBlocker || b.Records["spec-1"].Raised != 1 {
		t.Errorf("finding = %+v", got)
	}
}
