package jormungandr

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ouroboric-dev/gungnir"
)

// ReportError is a reviewer's report that parsed but does not fit the book: it drops a finding, closes
// one without proof, or speaks for another reviewer. Like an answer that does not parse, it is malformed
// output, which never counts as a pass (spec §4.6).
type ReportError struct {
	Reviewer string
	Reason   string
}

func (e *ReportError) Error() string { return fmt.Sprintf("reviewer %s: %s", e.Reviewer, e.Reason) }

// evidence matches a place in the code: a file and a line.
var evidence = regexp.MustCompile(`[^\s:]+:[0-9]+`)

// proves reports whether proof is what spec §4.6 asks for to close a finding: a file:line the reviewer
// read, and, when the loop has checks, the name of one that passed on this attempt. The reviewer can't
// run a check; Odin ran them before it asked, and tells the reviewer which passed.
func proves(proof string, passed []string) bool {
	if !evidence.MatchString(proof) {
		return false
	}
	if len(passed) == 0 {
		return true
	}
	return slices.ContainsFunc(passed, func(c string) bool {
		return regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(c) + `($|[^A-Za-z0-9_-])`).MatchString(proof)
	})
}

// Review applies a reviewer's report to the book. reviewer names the source; passed lists the checks
// that passed on this attempt. The rules, from spec §4.6:
//   - every finding in the report is the reviewer's own (its source is the reviewer's id);
//   - every finding the reviewer still held open is in the report, so none disappears unmentioned;
//   - a finding closes (status fixed) only with proof: a file:line and the name of a check that passed;
//   - a finding that is not open can't be closed, and a new finding can't be born closed;
//   - a closed finding reported open again is reopened.
//
// A report that breaks a rule changes nothing and is a *ReportError.
func (b *Book) Review(attempt int, reviewer string, passed []string, report gungnir.Report) ([]Change, error) {
	bad := func(format string, a ...any) ([]Change, error) {
		return nil, &ReportError{Reviewer: reviewer, Reason: fmt.Sprintf(format, a...)}
	}
	reported := map[string]bool{}
	for _, f := range report.Findings {
		reported[f.ID] = true
		old, known := b.Records[f.ID]
		switch {
		case f.Source != reviewer:
			return bad("finding %s says its source is %q", f.ID, f.Source)
		case known && old.Source != reviewer:
			return bad("finding %s belongs to %s", f.ID, old.Source)
		case f.Status == gungnir.FindingFixed && (!known || !old.isOpen()):
			return bad("finding %s is closed in the report but was not open", f.ID)
		case f.Status == gungnir.FindingFixed && !proves(f.Proof, passed):
			return bad("finding %s is closed without proof: the proof must cite a file:line and name a check that passed (%s)", f.ID, strings.Join(passed, ", "))
		}
	}
	for _, f := range b.OpenFrom(reviewer) {
		if !reported[f.ID] {
			return bad("finding %s is still open and the report doesn't mention it", f.ID)
		}
	}
	var changes []Change
	for _, f := range report.Findings {
		r, known := b.Records[f.ID]
		switch {
		case !known:
			b.put(f, attempt)
			changes = append(changes, Change{Kind: Raised, Finding: f})
		case f.Status == gungnir.FindingFixed:
			r.Finding, r.Closed = f, attempt
			changes = append(changes, Change{Kind: Closed, Finding: f, Attempts: r.Attempts(), Reopens: r.Reopens})
		case !r.isOpen():
			r.Reopens++
			r.Closed, r.Finding = 0, f
			changes = append(changes, Change{Kind: Reopened, Finding: f, Reopens: r.Reopens})
		default:
			r.Finding = f
		}
	}
	return changes, nil
}
