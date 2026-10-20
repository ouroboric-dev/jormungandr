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

// ReportFormat explains to a reviewer how to answer (spec §4.6). Its answer is read by a program, and
// anything that doesn't parse stops the loop.
const ReportFormat = `Answer with one JSON document: end your answer with the document; nothing after it. Any text before it is ignored. The document, bare or in one code fence:

{"findings": [ {"id": "...", "source": "<your id>", "severity": "blocker|major|minor", "location": "path/to/file:LINE", "claim": "what is wrong", "status": "open"} ]}

- "id" names the problem and stays the same every time you report it.
- "location" is a file and a line, or a range such as path/to/file:10-20.
- "status" is "open" for a problem you still see. Say "fixed" only for a problem listed below that you checked
  is gone, and then add "proof": the file:line you read, and the name of one of the checks listed below
  that passed. A finding without both is refused.
- A blocker or major finding of yours that the implementer disputed and a check settled is closed ("overruled"):
  it is not in the list below. Report it again only with proof: a failing check, or a different location than
  before. A repeat with neither is ignored.
- If you find nothing, answer {"findings": []}.`

// DisputeFormat explains to an implementer how to dispute a finding it believes is wrong. A dispute is settled
// by a check of the loop, never by its word against the reviewer's. Odin reads the document from the end of
// the implementer's final answer.
const DisputeFormat = `## Disputing a finding

If you believe a finding above is wrong, do not just say so, and do not change code to please it. Prove it with a test that one of this loop's checks runs and prints. End your answer with one JSON document; nothing after it. Any text before it is ignored:

{"disputes": [ {"id": "<finding id>", "check": "<check name>", "test": "<test id>", "evidence": "<what the test showed, and where>"} ]}

- "id" is a finding listed above. You can't dispute a finding raised by a check: fix the work.
- "check" is the name of one of this loop's checks. It must run and pass after your work.
- "test" is a test id that the check's output will show, such as the name of a test it runs. If the output does not contain it, the dispute does not hold.
- "evidence" says what the test showed and where in the code.
- A dispute that holds closes the finding. One that does not hold leaves it open, and you are told why. If you have no document, no finding changes.`

// WithDisputeFormat appends DisputeFormat to an implementer's prompt, and names the loop's checks, when there
// are open findings. With none there is nothing to dispute, and the prompt is unchanged.
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
// shown to it: a reviewer judges its own.
func ReviewRequest(reviewer string, open []gungnir.Finding, passed []string) string {
	var b strings.Builder
	b.WriteString("## Your answer\n\nYour id is " + reviewer + ". Use it as the \"source\" of every finding.\n\n" + ReportFormat + "\n\n")
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
	return b.String()
}
