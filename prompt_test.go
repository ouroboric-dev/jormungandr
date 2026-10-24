package jormungandr

import (
	"encoding/json"
	"fmt"
	"io/fs"
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
	got := reviewRequest(t, "spec", []gungnir.Finding{f}, []string{"vet", "test"}, true)
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
	if first := reviewRequest(t, "quality", nil, nil, true); strings.Contains(first, "Findings you reported before") || strings.Contains(first, "checks that passed") {
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

// instructions is AnswerInstructions for a contract that must have them.
func instructions(t *testing.T, c schema.Contract, viaSubmit bool) string {
	t.Helper()
	text, err := AnswerInstructions(c, viaSubmit)
	if err != nil {
		t.Fatalf("AnswerInstructions(%s, %v) = %v", c, viaSubmit, err)
	}
	return text
}

// reviewRequest is ReviewRequest, which must succeed.
func reviewRequest(t *testing.T, reviewer string, open []gungnir.Finding, passed []string, viaSubmit bool) string {
	t.Helper()
	text, err := ReviewRequest(reviewer, open, passed, viaSubmit)
	if err != nil {
		t.Fatalf("ReviewRequest() = %v", err)
	}
	return text
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
		tool := instructions(t, c.c, true)
		plain := instructions(t, c.c, false)
		name := "`" + string(c.c) + "`"
		for _, w := range append([]string{
			"Hand back your answer by calling the tool `submit` with it; it tells you what to fix. Your answer is a " + name + " document:",
			"required unless you are blocked", "only when the work cannot be done", "a person will see it",
			"The schema of a " + name + " document:",
		}, c.fields...) {
			if !strings.Contains(tool, w) {
				t.Errorf("%s with the tool lacks %q:\n%s", c.c, w, tool)
			}
		}
		for _, w := range append([]string{
			"Your whole final answer is one " + name + " JSON document and nothing else.",
			"only when the work cannot be done",
			"The schema of a " + name + " document:",
		}, c.fields...) {
			if !strings.Contains(plain, w) {
				t.Errorf("%s without the tool lacks %q:\n%s", c.c, w, plain)
			}
		}
		if strings.Contains(plain, "submit") || strings.Contains(tool, "Your whole final answer") {
			t.Errorf("%s: the modes mix:\n%s\n%s", c.c, tool, plain)
		}
		if !strings.Contains(tool, c.required+" (required") {
			t.Errorf("%s: %s is not marked required:\n%s", c.c, c.required, tool)
		}
	}
}

// schemaIn returns the schema that follows "The schema of a `c` document:" in text: one line of JSON.
func schemaIn(t *testing.T, c schema.Contract, text string) map[string]any {
	t.Helper()
	head := "The schema of a `" + string(c) + "` document:\n"
	i := strings.Index(text, head)
	if i < 0 {
		t.Fatalf("%s: no schema in:\n%s", c, text)
	}
	line, _, _ := strings.Cut(text[i+len(head):], "\n")
	var doc map[string]any
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatalf("%s: the schema is not one JSON document: %v\n%s", c, err, line)
	}
	return doc
}

// refs returns every $ref in a JSON value.
func refs(v any) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if s, ok := x.(string); ok && k == "$ref" {
				out = append(out, s)
				continue
			}
			out = append(out, refs(x)...)
		}
	case []any:
		for _, x := range v {
			out = append(out, refs(x)...)
		}
	}
	return out
}

// pointsAt reports whether the local reference ref ("#/a/b") names a value in doc.
func pointsAt(doc map[string]any, ref string) bool {
	var v any = doc
	for _, tok := range strings.Split(strings.TrimPrefix(ref, "#"), "/")[1:] {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if v, ok = m[strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)]; !ok {
			return false
		}
	}
	return true
}

func TestTheInstructionsOfEveryAgentOutputCarryItsWholeSchema(t *testing.T) {
	agentOutputs := []schema.Contract{
		schema.ContractDesign, schema.ContractSpec, schema.ContractConformance, schema.ContractPlan,
		schema.ContractReuseReview, schema.ContractReview, schema.ContractImplement, schema.ContractRetro,
	}
	for _, c := range agentOutputs {
		file, _ := c.SchemaFile()
		raw, err := fs.ReadFile(schema.SchemaFS(), file)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		var top struct{ Properties map[string]json.RawMessage }
		if err := json.Unmarshal(raw, &top); err != nil || len(top.Properties) == 0 {
			t.Fatalf("%s: properties %v, %v", c, top.Properties, err)
		}
		for _, viaSubmit := range []bool{true, false} {
			text := instructions(t, c, viaSubmit)
			for p := range top.Properties {
				if !strings.Contains(text, "- `"+p+"`") {
					t.Errorf("%s (submit %v): the field %s is not named:\n%s", c, viaSubmit, p, text)
				}
			}
			if !strings.Contains(text, "Use `blocked`") {
				t.Errorf("%s (submit %v): no word on blocked:\n%s", c, viaSubmit, text)
			}
			doc := schemaIn(t, c, text)
			if id, _ := doc["$id"].(string); !strings.HasSuffix(id, "/"+file) {
				t.Errorf("%s: the schema's $id is %v, want the contract's own", c, doc["$id"])
			}
			for _, r := range refs(doc) {
				if !strings.HasPrefix(r, "#") {
					t.Errorf("%s: the schema still refers to a file: %q", c, r)
				} else if !pointsAt(doc, r) {
					t.Errorf("%s: %q points at nothing in the bundled schema", c, r)
				}
			}
		}
	}
	// The review's findings are bundled whole: a model sees the finding's fields.
	doc := schemaIn(t, schema.ContractReview, instructions(t, schema.ContractReview, true))
	if !strings.Contains(fmt.Sprint(doc["$defs"]), "severity") {
		t.Errorf("the review schema lacks the finding's shape: %v", doc["$defs"])
	}
}

func TestTheBlockedLineComesOnlyWithABlockedField(t *testing.T) {
	text := instructions(t, schema.ContractCheck, false)
	if strings.Contains(text, "`blocked`") {
		t.Errorf("ygg.check/v1 has no blocked field, but the instructions offer it:\n%s", text)
	}
	schemaIn(t, schema.ContractCheck, text)
}

func TestTheInstructionsOfAnUnknownContractAreAnError(t *testing.T) {
	text, err := AnswerInstructions(schema.Contract("ygg.nope/v1"), true)
	if err == nil || text != "" || !strings.Contains(err.Error(), "ygg.nope/v1") {
		t.Errorf("AnswerInstructions(ygg.nope/v1) = %q, %v; want no text and an error naming the contract", text, err)
	}
}

func TestTheReviewRequestCarriesTheAnswerInstructionsAndTheRules(t *testing.T) {
	for _, viaSubmit := range []bool{true, false} {
		got := reviewRequest(t, "spec", nil, nil, viaSubmit)
		if !strings.Contains(got, instructions(t, schema.ContractReview, viaSubmit)) {
			t.Errorf("viaSubmit %v: no instructions:\n%s", viaSubmit, got)
		}
		for _, w := range []string{"Say \"fixed\" only", "proof"} {
			if !strings.Contains(got, w) {
				t.Errorf("lacks %q:\n%s", w, got)
			}
		}
	}
}
