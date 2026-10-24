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
	got := ReviewRequest("spec", []gungnir.Finding{f}, []string{"vet", "test"}, true)
	for _, want := range []string{"Your id is spec", "The checks that passed on this work: vet, test.", "## Findings you reported before", `"id": "spec-1"`, "One you leave out is an error"} {
		if !strings.Contains(got, want) {
			t.Errorf("ReviewRequest() lacks %q:\n%s", want, got)
		}
	}
	// The findings in the request are a valid report, so a reviewer that copies them back is understood.
	start := strings.Index(got, "```json\n") + len("```json\n")
	end := strings.LastIndex(got, "\n```")
	if rep, err := schema.Decode[schema.Review](schema.ContractReview, []byte(got[start:end])); err != nil || len(rep.Findings) != 1 || rep.Findings[0].ID != "spec-1" {
		t.Errorf("the findings in the request don't parse as a report: %+v, %v", rep, err)
	}
	if first := ReviewRequest("quality", nil, nil, true); strings.Contains(first, "Findings you reported before") || strings.Contains(first, "checks that passed") {
		t.Errorf("a first review has nothing to recall:\n%s", first)
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

func TestTheInstructionsNameTheContract(t *testing.T) {
	cases := []struct {
		c        schema.Contract
		required string
		fields   []string
	}{
		{schema.ContractReview, "`findings`", []string{"`findings`", "`blocked`", "`notes`", "Every finding the reviewer holds open or closes."}},
		{schema.ContractImplement, "`summary`", []string{"`summary`", "`disputes`", "`blocked`", "`notes`", "What the implementer did."}},
	}
	for _, c := range cases {
		tool := AnswerInstructions(c.c, true)
		plain := AnswerInstructions(c.c, false)
		name := "`" + string(c.c) + "`"
		for _, w := range append([]string{
			"Hand back your answer by calling the tool `submit` with it; it tells you what to fix. Your answer is a " + name + " document:",
			"required unless you are blocked", "only when the work cannot be done", "a person will see it",
		}, c.fields...) {
			if !strings.Contains(tool, w) {
				t.Errorf("%s with the tool lacks %q:\n%s", c.c, w, tool)
			}
		}
		for _, w := range append([]string{
			"Your whole final answer is one " + name + " JSON document and nothing else.",
			"only when the work cannot be done",
		}, c.fields...) {
			if !strings.Contains(plain, w) {
				t.Errorf("%s without the tool lacks %q:\n%s", c.c, w, plain)
			}
		}
		if strings.Contains(plain, "submit") || strings.Contains(tool, "nothing else") {
			t.Errorf("%s: the modes mix:\n%s\n%s", c.c, tool, plain)
		}
		if !strings.Contains(tool, c.required+" (required") {
			t.Errorf("%s: %s is not marked required:\n%s", c.c, c.required, tool)
		}
	}
	if got := AnswerInstructions(schema.ContractPlan, true); got == "" || !strings.Contains(got, "`ygg.plan/v1`") {
		t.Errorf("another contract: %q", got)
	}
}

func TestTheReviewRequestCarriesTheAnswerInstructionsAndTheRules(t *testing.T) {
	for _, viaSubmit := range []bool{true, false} {
		got := ReviewRequest("spec", nil, nil, viaSubmit)
		if !strings.Contains(got, AnswerInstructions(schema.ContractReview, viaSubmit)) {
			t.Errorf("viaSubmit %v: no instructions:\n%s", viaSubmit, got)
		}
		for _, w := range []string{"Say \"fixed\" only", "proof"} {
			if !strings.Contains(got, w) {
				t.Errorf("lacks %q:\n%s", w, got)
			}
		}
	}
}
