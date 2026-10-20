package jormungandr

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/ouroboric-dev/gungnir"
)

// Record is one finding and its history in a loop (spec §4.6, §10.1): the attempt it was first
// reported, the attempt it closed, and how often it came back.
type Record struct {
	gungnir.Finding
	Raised  int `json:"raised"`            // the attempt the finding was first reported
	Closed  int `json:"closed,omitempty"`  // the attempt its source confirmed the fix; 0 while it is open
	Reopens int `json:"reopens,omitempty"` // how many times it was reported again after it closed
	// OverruledBy names the check whose pass settled the implementer's dispute; empty unless the finding is
	// overruled. CheckFailedSince is true once that check failed in a later attempt: from then on a reviewer's
	// repeat of the claim reopens the finding.
	OverruledBy      string `json:"overruled_by,omitempty"`
	CheckFailedSince bool   `json:"check_failed_since,omitempty"`
}

// Attempts is how many attempts the finding took to close, counting the one that raised it and the one
// that closed it. It is 0 for a finding that is still open.
func (r Record) Attempts() int {
	if r.Closed == 0 {
		return 0
	}
	return r.Closed - r.Raised + 1
}

func (r Record) isOpen() bool {
	return r.Status != gungnir.FindingFixed && r.Status != gungnir.FindingOverruled
}

// Book holds a loop's findings by id. Its only way to close a finding is the source's confirmation:
// a check that passes, or a reviewer's report with proof. An implementer's claim never closes one.
type Book struct {
	Records map[string]*Record `json:"records,omitempty"`
}

// ChangeKind says what happened to a finding.
type ChangeKind string

const (
	Raised   ChangeKind = "raised"   // first reported
	Closed   ChangeKind = "closed"   // its source confirmed the fix, with proof
	Reopened ChangeKind = "reopened" // reported again after it closed
	// Overruled: the implementer's dispute held, a named check passed and printed the test (the finding_overruled event).
	Overruled ChangeKind = "overruled"
	// DisputeIgnored: a dispute, or the document that held it, could not be used; Reason says why, and begins
	// "dispute ignored: ". It changes no finding.
	DisputeIgnored ChangeKind = "dispute-ignored"
)

// Change is one change to the book, for a ledger line (spec §10.1).
type Change struct {
	Kind    ChangeKind
	Finding gungnir.Finding
	// Attempts and Reopens are set on a Closed change (the attempts it took, and how often it came
	// back) and Reopens on a Reopened one.
	Attempts int
	Reopens  int
	// Check is the check that settled an Overruled change; Reason is the why of a DisputeIgnored one.
	Check  string
	Reason string
}

func (b *Book) put(f gungnir.Finding, attempt int) *Record {
	if b.Records == nil {
		b.Records = map[string]*Record{}
	}
	r := &Record{Finding: f, Raised: attempt}
	b.Records[f.ID] = r
	return r
}

// severityRank orders findings, worst first.
func severityRank(s gungnir.Severity) int {
	switch s {
	case gungnir.SeverityBlocker:
		return 0
	case gungnir.SeverityMajor:
		return 1
	}
	return 2
}

func (b *Book) sorted(keep func(Record) bool) []gungnir.Finding {
	var recs []Record
	for _, r := range b.Records {
		if keep(*r) {
			recs = append(recs, *r)
		}
	}
	slices.SortFunc(recs, func(x, y Record) int {
		return cmp.Or(cmp.Compare(severityRank(x.Severity), severityRank(y.Severity)), cmp.Compare(x.ID, y.ID))
	})
	out := make([]gungnir.Finding, len(recs))
	for i, r := range recs {
		out[i] = r.Finding
	}
	return out
}

// Open returns every finding not yet closed, the worst first and by id within a severity, so the
// order is the same on every attempt.
func (b *Book) Open() []gungnir.Finding { return b.sorted(Record.isOpen) }

// OpenFrom returns the open findings a source raised.
func (b *Book) OpenFrom(source string) []gungnir.Finding {
	return b.sorted(func(r Record) bool { return r.isOpen() && r.Source == source })
}

// Blocking returns the open findings that stop a loop from passing: blockers and majors. A minor
// finding is recorded, and goes back to the implementer with the others, but never fails a loop alone
// (Phase 4, decision 9).
func (b *Book) Blocking() []gungnir.Finding {
	return b.sorted(func(r Record) bool { return r.isOpen() && r.Severity != gungnir.SeverityMinor })
}

// All returns every record, closed or open, by id, for a report.
func (b *Book) All() []Record {
	var out []Record
	for _, r := range b.Records {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(x, y Record) int { return cmp.Compare(x.ID, y.ID) })
	return out
}

// CheckPrefix starts the id of every finding a check raises: check-<name>. A reviewer's finding can't
// use it, so the two sources never share an id.
const CheckPrefix = "check-"

// ConflictError means a check met a finding of another source under its own id. The book is left as it
// was: a check never closes, reopens or takes over a finding it did not raise.
type ConflictError struct {
	Check string
	ID    string
	Owner string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("check %s: finding %s belongs to %s", e.Check, e.ID, e.Owner)
}

// CheckFailed records that a check did not pass on this attempt. The same check keeps the same
// finding; its claim and location follow the latest output. A finding that had closed is reopened. If
// the id belongs to another source, nothing changes and the error is a *ConflictError.
func (b *Book) CheckFailed(attempt int, f gungnir.Finding) ([]Change, error) {
	for _, o := range b.Records {
		if o.Status == gungnir.FindingOverruled && o.OverruledBy == f.Source && attempt > o.Closed {
			o.CheckFailedSince = true // the check that settled the dispute no longer passes
		}
	}
	r, ok := b.Records[f.ID]
	switch {
	case !ok:
		b.put(f, attempt)
		return []Change{{Kind: Raised, Finding: f}}, nil
	case r.Source != f.Source:
		return nil, &ConflictError{Check: f.Source, ID: f.ID, Owner: r.Source}
	case !r.isOpen():
		r.Reopens++
		r.Closed = 0
		r.Finding = f
		return []Change{{Kind: Reopened, Finding: f, Reopens: r.Reopens}}, nil
	}
	r.Finding = f
	return nil, nil
}

// CheckPassed closes the finding the check raised earlier, with proof that the check ran and passed. A
// check that never failed has nothing to close. If the id belongs to another source, nothing changes
// and the error is a *ConflictError.
func (b *Book) CheckPassed(attempt int, check, id, proof string) ([]Change, error) {
	r, ok := b.Records[id]
	if !ok {
		return nil, nil
	}
	if r.Source != check {
		return nil, &ConflictError{Check: check, ID: id, Owner: r.Source}
	}
	if !r.isOpen() {
		return nil, nil
	}
	r.Status, r.Proof, r.Closed = gungnir.FindingFixed, proof, attempt
	return []Change{{Kind: Closed, Finding: r.Finding, Attempts: r.Attempts(), Reopens: r.Reopens}}, nil
}

// Overrule applies a dispute that holds: the finding closes as overruled, with the implementer's evidence and
// the proof of the check run (what Dispute returned) as its proof, and the book remembers which check settled
// it. Only an open finding of a reviewer can be overruled: a check's finding is deterministic and a person
// fixes the check in the realm. Nothing changes on an error.
func (b *Book) Overrule(attempt int, d gungnir.Dispute, checkProof string) ([]Change, error) {
	r, ok := b.Records[d.ID]
	switch {
	case !ok:
		return nil, fmt.Errorf("finding %s is not in the book", d.ID)
	case strings.HasPrefix(d.ID, CheckPrefix):
		return nil, fmt.Errorf("finding %s was raised by a check, which a dispute can't overrule", d.ID)
	case !r.isOpen():
		return nil, fmt.Errorf("finding %s is not open", d.ID)
	}
	proof := fmt.Sprintf("Implementer's evidence: %s\nCheck %s ran and passed in attempt %d: %s", d.Evidence, d.Check, attempt, strings.TrimSpace(checkProof))
	if runes := []rune(proof); len(runes) > gungnir.MaxProof {
		proof = string(runes[:gungnir.MaxProof])
	}
	r.Status, r.Proof, r.Closed = gungnir.FindingOverruled, proof, attempt
	r.OverruledBy, r.CheckFailedSince = d.Check, false
	return []Change{{Kind: Overruled, Finding: r.Finding, Check: d.Check, Attempts: r.Attempts(), Reopens: r.Reopens}}, nil
}
