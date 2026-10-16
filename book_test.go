package jormungandr

import (
	"encoding/json"
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
	if ch := b.CheckFailed(1, f); len(ch) != 1 || ch[0].Kind != Raised || ch[0].Finding.ID != "check-test" {
		t.Fatalf("first failure: %+v", ch)
	}
	f.Claim, f.Location = "newer output", "logs/b-2-test.log:1"
	if ch := b.CheckFailed(2, f); len(ch) != 0 {
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
	if ch := b.CheckPassed(2, "check-vet", "x"); len(ch) != 0 {
		t.Errorf("a check with no finding closed something: %+v", ch)
	}
	ch := b.CheckPassed(3, "check-test", "check test: exit 0 (logs/b-3-test.log)")
	if len(ch) != 1 || ch[0].Kind != Closed || ch[0].Attempts != 3 || ch[0].Finding.Status != gungnir.FindingFixed || ch[0].Finding.Proof == "" {
		t.Fatalf("CheckPassed() = %+v; want a close after 3 attempts with proof", ch)
	}
	if len(b.Open()) != 0 || len(b.Blocking()) != 0 {
		t.Errorf("the book still holds %v", ids(b.Open()))
	}
	if ch := b.CheckPassed(4, "check-test", "again"); len(ch) != 0 {
		t.Errorf("a closed finding closed again: %+v", ch)
	}
}

func TestAClosedFindingThatComesBackIsReopenedAndCounted(t *testing.T) {
	var b Book
	f := finding("check-test", "test", gungnir.SeverityBlocker)
	b.CheckFailed(1, f)
	b.CheckPassed(2, f.ID, "exit 0")
	ch := b.CheckFailed(3, f)
	if len(ch) != 1 || ch[0].Kind != Reopened || ch[0].Reopens != 1 {
		t.Fatalf("CheckFailed() = %+v; want a reopen", ch)
	}
	r := b.Records[f.ID]
	if r.Status != gungnir.FindingOpen || r.Closed != 0 || r.Reopens != 1 || r.Raised != 1 {
		t.Errorf("record = %+v", r)
	}
	ch = b.CheckPassed(5, f.ID, "exit 0")
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
	b.CheckPassed(2, "check-test", "exit 0")
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
