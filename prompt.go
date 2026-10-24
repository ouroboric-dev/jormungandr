package jormungandr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/ouroboric-dev/gungnir"
	"github.com/ouroboric-dev/gungnir/schema"
)

// Feedback is the text added to an implementer's prompt after the first attempt (spec §4.6): the
// findings still open, why the last attempt failed if it did, and what an operator wrote when they sent
// the work back. Every session starts clean (spec §4.4), so this is all the next attempt knows of the
// last. The text tells the implementer it can't close a finding: the finding's source decides.
func Feedback(open []gungnir.Finding, lastFailure, guidance string) string {
	return FeedbackWith(open, nil, lastFailure, guidance)
}

// FeedbackWith is Feedback plus the implementer's disputes that did not hold in the last attempt (Input.Rejected):
// the finding stays open, and the text says why the dispute failed.
func FeedbackWith(open []gungnir.Finding, rejected []Rejection, lastFailure, guidance string) string {
	var b strings.Builder
	if len(open) > 0 {
		b.WriteString("## Findings to fix\n\nThe checks and reviewers found these problems in the work so far. Fix every one. " +
			"Do not say any of them is fixed: the check or reviewer that raised it looks again after your work and decides.\n\n")
		for _, f := range open {
			fmt.Fprintf(&b, "- %s (%s, from %s) at %s", f.ID, f.Severity, f.Source, f.Location)
			if f.Status == gungnir.FindingDisputed {
				b.WriteString(" [disputed]")
			}
			b.WriteString("\n")
			for _, line := range strings.Split(strings.TrimSpace(f.Claim), "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
		b.WriteString("\n")
	}
	if len(rejected) > 0 {
		b.WriteString("## Your disputes that did not hold\n\nYou said these findings are wrong. No check settled it, so each finding stays open. " +
			"Fix it, or dispute it again with a check of this loop that runs a test and prints its id.\n\n")
		for _, r := range rejected {
			fmt.Fprintf(&b, "- %s: check %s, test %s: %s\n", r.ID, r.Check, r.Test, strings.TrimSpace(r.Reason))
		}
		b.WriteString("\n")
	}
	if lastFailure != "" {
		fmt.Fprintf(&b, "## The last attempt failed\n\nYour last session ended with: %s\nThe repository is as the last attempt that finished left it.\n\n", lastFailure)
	}
	if guidance != "" {
		fmt.Fprintf(&b, "## Guidance from the operator\n\n%s\n\n", strings.TrimSpace(guidance))
	}
	return b.String()
}

// Refusal is the text added to an implementer's prompt when a step after the loop refused its work, such
// as the merge gate (spec §4.5): what the step said, so the attempt can fix it. It is empty for no
// refusal.
func Refusal(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "## Refused after your work passed\n\nYour work passed its checks and reviewers, and then a later step refused it. " +
		"Fix what it says. Do not say it is fixed: the step looks again after your work and decides.\n\n" + text + "\n\n"
}

// AnswerInstructions tells a session how to hand back its answer, which is a document of contract c. The
// fields come from the contract's embedded schema: its top-level properties, in the schema's order, with their
// descriptions, and the ones the schema requires when the session is not blocked. The schema itself follows, as
// one compact JSON document that refers to no other file, so the session knows every nested shape. viaSubmit says
// the session has the `submit` tool, which checks the document and tells it what to fix; without it, the final
// answer is the document. A contract with no embedded schema is an error.
func AnswerInstructions(c schema.Contract, viaSubmit bool) (string, error) {
	name, ok := c.SchemaFile()
	if !ok {
		return "", fmt.Errorf("contract %s has no embedded schema", c)
	}
	fields, err := answerFields(name)
	if err != nil {
		return "", fmt.Errorf("contract %s: %w", c, err)
	}
	bundled, err := bundledSchema(name)
	if err != nil {
		return "", fmt.Errorf("contract %s: %w", c, err)
	}
	var b strings.Builder
	if viaSubmit {
		fmt.Fprintf(&b, "Hand back your answer by calling the tool `submit` with it; it tells you what to fix. Your answer is a `%s` document:\n", c)
	} else {
		fmt.Fprintf(&b, "Your whole final answer is one `%s` JSON document and nothing else. Its fields:\n", c)
	}
	blocked := false
	for _, f := range fields {
		fmt.Fprintf(&b, "- `%s`", f.name)
		if f.required {
			b.WriteString(" (required unless you are blocked)")
		}
		if f.description != "" {
			b.WriteString(": " + f.description)
		}
		b.WriteString("\n")
		blocked = blocked || f.name == "blocked"
	}
	fmt.Fprintf(&b, "The schema of a `%s` document:\n%s", c, bundled)
	if blocked {
		b.WriteString("\nUse `blocked`, with a reason, only when the work cannot be done: a person will see it.")
	}
	return b.String(), nil
}

// answerField is one top-level property of a contract's schema.
type answerField struct {
	name, description string
	required          bool // required when the document is not blocked
}

// answerFields reads the top-level properties of the embedded schema file name, in the order the schema lists
// them. A property that is a local $ref takes the description of what it points at.
func answerFields(name string) ([]answerField, error) {
	raw, err := fs.ReadFile(schema.SchemaFS(), name)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Properties orderedProperties
		Else       struct{ Required []string }             `json:"else"`
		Defs       map[string]struct{ Description string } `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var fields []answerField
	for _, p := range doc.Properties {
		desc := p.Description
		if desc == "" {
			desc = doc.Defs[strings.TrimPrefix(p.Ref, "#/$defs/")].Description
		}
		fields = append(fields, answerField{name: p.name, description: desc, required: slices.Contains(doc.Else.Required, p.name)})
	}
	return fields, nil
}

// bundledSchema returns the embedded schema file name as one compact JSON document. Each other embedded file it
// refers to by $ref, directly or through another, goes into its $defs under the file's name (finding.schema.json
// becomes #/$defs/finding), without its own $id and $schema, and every $ref is rewritten to point inside the one
// document.
func bundledSchema(name string) (string, error) {
	root, err := readSchema(name)
	if err != nil {
		return "", err
	}
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		defs = map[string]any{}
	}
	keys := map[string]string{} // embedded file: its key in $defs
	var embed func(file string) (string, error)
	var rewrite func(v any, self string) error
	embed = func(file string) (string, error) {
		if k, ok := keys[file]; ok {
			return k, nil
		}
		k := strings.TrimSuffix(file, ".schema.json")
		for defs[k] != nil {
			k += "_"
		}
		s, err := readSchema(file)
		if err != nil {
			return "", err
		}
		delete(s, "$id")
		delete(s, "$schema")
		keys[file], defs[k] = k, s
		return k, rewrite(s, "#/$defs/"+k)
	}
	// rewrite points every $ref in v inside the bundle; self is where v's own document now sits ("#" for the root).
	rewrite = func(v any, self string) error {
		switch v := v.(type) {
		case map[string]any:
			for key, x := range v {
				ref, isRef := x.(string)
				if key != "$ref" || !isRef {
					if err := rewrite(x, self); err != nil {
						return err
					}
					continue
				}
				file, frag, _ := strings.Cut(ref, "#")
				if file == "" {
					v[key] = self + frag
					continue
				}
				if strings.ContainsAny(file, ":/") {
					return fmt.Errorf("%s: can't bundle $ref %q: not a file beside it", name, ref)
				}
				k, err := embed(file)
				if err != nil {
					return err
				}
				if v[key] = "#/$defs/" + k + frag; k == "" {
					v[key] = "#" + frag // the root itself
				}
			}
		case []any:
			for _, x := range v {
				if err := rewrite(x, self); err != nil {
					return err
				}
			}
		}
		return nil
	}
	keys[name] = "" // the root itself
	if err := rewrite(root, "#"); err != nil {
		return "", err
	}
	if len(defs) > 0 {
		root["$defs"] = defs
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

// readSchema reads an embedded schema file as a JSON object, keeping its numbers as written.
func readSchema(name string) (map[string]any, error) {
	raw, err := fs.ReadFile(schema.SchemaFS(), name)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return doc, nil
}

// orderedProperties is a schema's "properties" object, kept in the order of the file.
type orderedProperties []struct {
	name, Description, Ref string
}

func (o *orderedProperties) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var p struct {
			Description string `json:"description"`
			Ref         string `json:"$ref"`
		}
		if err := dec.Decode(&p); err != nil {
			return err
		}
		*o = append(*o, struct{ name, Description, Ref string }{key.(string), p.Description, p.Ref})
	}
	return nil
}

// reviewRules are what a reviewer needs to know about its findings besides their shape, which the schema holds.
const reviewRules = `- "id" names the problem and stays the same every time you report it.
- "location" is a file and a line, or a range such as path/to/file:10-20.
- "status" is "open" for a problem you still see. Say "fixed" only for a problem listed below that you checked
  is gone, and then add "proof": the file:line you read, and the name of one of the checks listed below
  that passed. A finding without both is refused.
- A blocker or major finding of yours that the implementer disputed and a check settled is closed ("overruled"):
  it is not in the list below. Report it again only with proof: a failing check, or a different location than
  before. A repeat with neither is ignored.
- If you find nothing, answer with an empty list of findings.`

// DisputeFormat explains to an implementer how to dispute a finding it believes is wrong. A dispute is settled
// by a check of the loop, never by its word against the reviewer's. It goes in the answer's `disputes` field;
// AnswerInstructions says how to hand the answer back, and the caller adds it to every implementer prompt itself
// (WithDisputeFormat does not).
const DisputeFormat = `## Disputing a finding

If you believe a finding above is wrong, do not just say so, and do not change code to please it. Prove it with a test that one of this loop's checks runs, and put the dispute in the ` + "`disputes`" + ` field of your answer. Each dispute has four fields:

- "id" is a finding listed above. You can't dispute a finding raised by a check: fix the work.
- "check" is the name of one of this loop's checks. It must run after your work.
- "test" is the id of a test that check runs, as the check's result names it. The dispute holds when the named check runs in the attempt and its result lists the named test among the tests that passed. If the check does not run, still fails that test, or does not list it as passed, the dispute does not hold.
- "evidence" says what the test showed and where in the code.

A dispute that holds closes the finding. One that does not hold leaves it open, and you are told why. If you have no disputes, leave the field out or empty: no finding changes.`

// WithDisputeFormat appends DisputeFormat to an implementer's prompt, and names the loop's checks, when there
// are open findings. With none there is nothing to dispute, and the prompt is unchanged. It does not say how to
// hand the answer back: the caller adds AnswerInstructions(schema.ContractImplement, viaSubmit) to every
// implementer prompt itself.
func WithDisputeFormat(prompt string, open []gungnir.Finding, checks []string) string {
	if len(open) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	if !strings.HasSuffix(prompt, "\n\n") {
		b.WriteString("\n\n")
	}
	b.WriteString(DisputeFormat + "\n\n")
	if len(checks) > 0 {
		b.WriteString("The checks of this loop: " + strings.Join(checks, ", ") + ".\n")
	} else {
		b.WriteString("This loop has no checks, so a dispute can't be settled: it stops the run for a person to decide.\n")
	}
	return b.String()
}

// ReviewRequest is the text added to a reviewer's prompt: how to answer, the findings it reported
// before, and the checks that passed on the work it is about to read. Findings of other sources are not
// shown to it: a reviewer judges its own. viaSubmit says the reviewer has the `submit` tool. The error is
// AnswerInstructions'.
func ReviewRequest(reviewer string, open []gungnir.Finding, passed []string, viaSubmit bool) (string, error) {
	answer, err := AnswerInstructions(schema.ContractReview, viaSubmit)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("## Your answer\n\nYour id is " + reviewer + ". Use it as the \"source\" of every finding.\n\n" +
		answer + "\n\n" + reviewRules + "\n\n")
	if len(passed) > 0 {
		b.WriteString("The checks that passed on this work: " + strings.Join(passed, ", ") + ".\n\n")
	}
	b.WriteString("The implementer may dispute one of your findings. A dispute is settled by a check: if it holds, the finding is overruled and closed. " +
		"If you see the same problem again, report it with proof: a failing check, or a different location than the overruled finding. A repeat with neither is ignored.\n\n")
	if len(open) > 0 {
		raw, _ := json.MarshalIndent(gungnir.Report{Findings: open}, "", "  ")
		b.WriteString("## Findings you reported before and still hold open\n\nReport each of these again, with the same id: as open if the problem is still there, or as fixed with proof if it is gone. " +
			"One you leave out is an error.\n\n```json\n" + string(raw) + "\n```\n")
	}
	return b.String(), nil
}
