package jormungandr

import (
	"strings"
	"testing"

	"github.com/ouroboric-dev/gungnir"
	"github.com/ouroboric-dev/gungnir/schema"
)

func TestFeedbackListsTheOpenFindingsAndDoesNotLetTheImplementerCloseThem(t *testing.T) {
	multi := finding("check-test", "test", gungnir.SeverityBlocker)
	multi.Claim = "check test failed: exit status 1\nthe end of its output:\n--- FAIL: TestThrottle"
	disputed := finding("spec-2", "spec", gungnir.SeverityMinor)
	disputed.Status = gungnir.FindingDisputed
	got := Feedback([]gungnir.Finding{multi, disputed}, "timed out after 20m0s", "Keep the public API unchanged.")
	for _, want := range []string{
		"## Findings to fix", "- check-test (blocker, from test) at api/auth.go:42", "  --- FAIL: TestThrottle",
		"- spec-2 (minor, from spec) at api/auth.go:42 [disputed]", "Do not say any of them is fixed",
		"## The last attempt failed", "timed out after 20m0s", "## Guidance from the operator", "Keep the public API unchanged.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Feedback() lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "check-test") > strings.Index(got, "spec-2") {
		t.Error("the findings are not in the order they were given")
	}
}

func TestFeedbackIsEmptyWhenThereIsNothingToSay(t *testing.T) {
	if got := Feedback(nil, "", ""); got != "" {
		t.Errorf("Feedback() = %q, want nothing", got)
	}
}

func TestAReviewRequestShowsTheReviewerItsOwnFindingsAndTheChecksThatPassed(t *testing.T) {
	f := finding("spec-1", "spec", gungnir.SeverityMajor)
	got := ReviewRequest("spec", []gungnir.Finding{f}, []string{"vet", "test"})
	for _, want := range []string{"Your id is spec", "The checks that passed on this work: vet, test.", "## Findings you reported before", `"id": "spec-1"`, "One you leave out is an error"} {
		if !strings.Contains(got, want) {
			t.Errorf("ReviewRequest() lacks %q:\n%s", want, got)
		}
	}
	// The findings in the request are a valid report, so a reviewer that copies them back is understood.
	start := strings.Index(got, "```json\n") + len("```json\n")
	end := strings.LastIndex(got, "\n```")
	if rep, err := schema.ParseReport([]byte(got[start:end])); err != nil || len(rep.Findings) != 1 || rep.Findings[0].ID != "spec-1" {
		t.Errorf("the findings in the request don't parse as a report: %+v, %v", rep, err)
	}
	if first := ReviewRequest("quality", nil, nil); strings.Contains(first, "Findings you reported before") || strings.Contains(first, "checks that passed") {
		t.Errorf("a first review has nothing to recall:\n%s", first)
	}
}

func TestTheReportFormatTheReviewerIsGivenIsTheOneParseReportReads(t *testing.T) {
	example := ReportFormat[strings.Index(ReportFormat, `{"findings": [ {`):]
	example = example[:strings.Index(example, "\n")]
	example = strings.NewReplacer(`"<your id>"`, `"spec"`, `"blocker|major|minor"`, `"major"`, `"path/to/file:LINE"`, `"path/to/file:12"`).Replace(example)
	if _, err := schema.ParseReport([]byte(example)); err != nil {
		t.Errorf("the example in ReportFormat does not parse: %v\n%s", err, example)
	}
}

func TestARefusalGoesIntoTheNextAttemptsPrompt(t *testing.T) {
	got := Refusal("merge gate refused (conflict): CONFLICT in api/auth.go")
	for _, want := range []string{"## Refused after your work passed", "merge gate refused (conflict)", "CONFLICT in api/auth.go", "Do not say"} {
		if !strings.Contains(got, want) {
			t.Errorf("Refusal() lacks %q:\n%s", want, got)
		}
	}
	if Refusal("  \n") != "" {
		t.Error("an empty refusal says something")
	}
}
