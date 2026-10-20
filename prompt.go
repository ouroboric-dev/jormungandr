package jormungandr

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ouroboric-dev/gungnir"
)

// Feedback is the text added to an implementer's prompt after the first attempt (spec §4.6): the
// findings still open, why the last attempt failed if it did, and what an operator wrote when they sent
// the work back. Every session starts clean (spec §4.4), so this is all the next attempt knows of the
// last. The text tells the implementer it can't close a finding: the finding's source decides.
func Feedback(open []gungnir.Finding, lastFailure, guidance string) string {
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

// ReportFormat explains to a reviewer how to answer (spec §4.6). Its answer is read by a program, and
// anything that doesn't parse stops the loop.
const ReportFormat = `Answer with one JSON document: end your answer with the document; nothing after it. Any text before it is ignored. The document, bare or in one code fence:

{"findings": [ {"id": "...", "source": "<your id>", "severity": "blocker|major|minor", "location": "path/to/file:LINE", "claim": "what is wrong", "status": "open"} ]}

- "id" names the problem and stays the same every time you report it.
- "location" is a file and a line, or a range such as path/to/file:10-20.
- "status" is "open" for a problem you still see. Say "fixed" only for a problem listed below that you checked
  is gone, and then add "proof": the file:line you read, and the name of one of the checks listed below
  that passed. A finding without both is refused.
- If you find nothing, answer {"findings": []}.`

// ReviewRequest is the text added to a reviewer's prompt: how to answer, the findings it reported
// before, and the checks that passed on the work it is about to read. Findings of other sources are not
// shown to it: a reviewer judges its own.
func ReviewRequest(reviewer string, open []gungnir.Finding, passed []string) string {
	var b strings.Builder
	b.WriteString("## Your answer\n\nYour id is " + reviewer + ". Use it as the \"source\" of every finding.\n\n" + ReportFormat + "\n\n")
	if len(passed) > 0 {
		b.WriteString("The checks that passed on this work: " + strings.Join(passed, ", ") + ".\n\n")
	}
	if len(open) > 0 {
		raw, _ := json.MarshalIndent(gungnir.Report{Findings: open}, "", "  ")
		b.WriteString("## Findings you reported before and still hold open\n\nReport each of these again, with the same id: as open if the problem is still there, or as fixed with proof if it is gone. " +
			"One you leave out is an error.\n\n```json\n" + string(raw) + "\n```\n")
	}
	return b.String()
}
