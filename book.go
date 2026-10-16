package jormungandr

import (
	"cmp"
	"slices"

	"github.com/ouroboric-dev/gungnir"
)

// Record is one finding and its history in a loop (spec §4.6, §10.1): the attempt it was first
// reported, the attempt it closed, and how often it came back.
type Record struct {
	gungnir.Finding
	Raised  int `json:"raised"`            // the attempt the finding was first reported
	Closed  int `json:"closed,omitempty"`  // the attempt its source confirmed the fix; 0 while it is open
	Reopens int `json:"reopens,omitempty"` // how many times it was reported again after it closed
}

// Attempts is how many attempts the finding took to close, counting the one that raised it and the one
// that closed it. It is 0 for a finding that is still open.
func (r Record) Attempts() int {
	if r.Closed == 0 {
		return 0
	}
	return r.Closed - r.Raised + 1
}

func (r Record) isOpen() bool { return r.Status != gungnir.FindingFixed }

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
)

// Change is one change to the book, for a ledger line (spec §10.1).
type Change struct {
	Kind    ChangeKind
	Finding gungnir.Finding
	// Attempts and Reopens are set on a Closed change (the attempts it took, and how often it came
	// back) and Reopens on a Reopened one.
	Attempts int
	Reopens  int
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

// CheckFailed records that a check did not pass on this attempt. The same check keeps the same
// finding; its claim and location follow the latest output. A finding that had closed is reopened.
func (b *Book) CheckFailed(attempt int, f gungnir.Finding) []Change {
	r, ok := b.Records[f.ID]
	switch {
	case !ok:
		b.put(f, attempt)
		return []Change{{Kind: Raised, Finding: f}}
	case !r.isOpen():
		r.Reopens++
		r.Closed = 0
		r.Finding = f
		return []Change{{Kind: Reopened, Finding: f, Reopens: r.Reopens}}
	}
	r.Finding = f
	return nil
}

// CheckPassed closes the finding a check raised earlier, with proof that the check ran and passed. A
// check that never failed has nothing to close.
func (b *Book) CheckPassed(attempt int, id, proof string) []Change {
	r, ok := b.Records[id]
	if !ok || !r.isOpen() {
		return nil
	}
	r.Status, r.Proof, r.Closed = gungnir.FindingFixed, proof, attempt
	return []Change{{Kind: Closed, Finding: r.Finding, Attempts: r.Attempts(), Reopens: r.Reopens}}
}
