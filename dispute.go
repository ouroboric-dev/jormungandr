package jormungandr

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ouroboric-dev/gungnir"
)

// Verdict is how a dispute was decided.
type Verdict string

const (
	// Holds: the named check ran in this attempt and its result lists the disputed test among the tests that passed.
	Holds Verdict = "holds"
	// DoesNotHold: the check did not run, still fails the test, or does not list it as passed. The finding stays open.
	DoesNotHold Verdict = "does-not-hold"
	// Unsettleable: no check of the loop can decide it. The loop escalates as disputed.
	Unsettleable Verdict = "unsettleable"
)

// DisputeOutcome is how one dispute was decided.
type DisputeOutcome struct {
	Verdict Verdict
	Proof   string // Holds: what the check's result showed, which goes into the finding's proof
	Reason  string // DoesNotHold and Unsettleable: why
}

// SettleDispute is the rule that decides a dispute (spec §4.6, ruling R23). ran holds the outcomes of the checks
// that ran in this attempt, by check name, and only those: an outcome of an earlier attempt proves nothing about
// this one. The dispute holds only when the named check ran and its typed result lists the disputed test among
// the tests that passed (PassedTests), and not among those that failed. It does not hold when the check did not
// run, still fails the test, failed and named no failing test (it says nothing about this one), lists no passing
// tests (it can't prove the test ran), or does not list the test as passed.
func SettleDispute(attempt int, d gungnir.Dispute, ran map[string]CheckOutcome) DisputeOutcome {
	notHeld := func(format string, a ...any) DisputeOutcome {
		return DisputeOutcome{Verdict: DoesNotHold, Reason: fmt.Sprintf(format, a...)}
	}
	co, ok := ran[d.Check]
	switch {
	case !ok:
		return notHeld("check %s did not run in attempt %d", d.Check, attempt)
	case slices.Contains(co.Failed, d.Test):
		return notHeld("check %s ran in attempt %d and still fails %s", d.Check, attempt, d.Test)
	case !co.Passed && len(co.Failed) == 0:
		return notHeld("check %s failed in attempt %d and names no failing test, so it can't show that %s passes", d.Check, attempt, d.Test)
	case len(co.PassedTests) == 0:
		return notHeld("check %s ran in attempt %d, but its result lists no passing tests, so it can't show that %s ran and passed", d.Check, attempt, d.Test)
	case !slices.Contains(co.PassedTests, d.Test):
		return notHeld("check %s ran in attempt %d and %s is not among the tests that passed", d.Check, attempt, d.Test)
	}
	return DisputeOutcome{Verdict: Holds, Proof: fmt.Sprintf("check %s ran in attempt %d and %s is among the tests that passed", d.Check, attempt, d.Test)}
}

// Rejection is a dispute that did not hold, with the reason, kept so the next attempt is told and the
// escalation can show both sides.
type Rejection struct {
	gungnir.Dispute
	Reason string `json:"reason"`
}

// settleDisputes decides the attempt's disputes, one by one, after the checks ran and before the reviewers, on
// ran, the outcomes of the checks that ran in this attempt (SettleDispute). A dispute that holds overrules its
// finding; one that doesn't is remembered; one that can't be used is recorded and ignored. A dispute that names a
// check the loop does not have is unsettleable: when any is, the loop escalates, after the rest were decided. stop
// is true when the loop ends here.
func settleDisputes(cfg Config, st *State, steps Steps, ran map[string]CheckOutcome) (out Outcome, stop bool, err error) {
	var unsettled []string
	for len(st.Disputes) > 0 {
		d := st.Disputes[0]
		var changes []Change
		ignore := func(format string, a ...any) {
			changes = append(changes, Change{Kind: DisputeIgnored, Reason: "dispute ignored: " + fmt.Sprintf(format, a...)})
		}
		r, known := st.Book.Records[d.ID]
		switch {
		case !known || !r.isOpen():
			ignore("finding %s is not open", d.ID)
		case strings.HasPrefix(d.ID, CheckPrefix):
			ignore("finding %s was raised by a check, and a failing check is fixed, not disputed", d.ID)
		default:
			o := SettleDispute(st.Attempt, d, ran)
			if !slices.Contains(cfg.Checks, d.Check) {
				o = DisputeOutcome{Verdict: Unsettleable, Reason: fmt.Sprintf("check %q is not one of this loop's checks (%s), so no check can settle it", d.Check, strings.Join(cfg.Checks, ", "))}
			}
			switch o.Verdict {
			case Holds:
				var cerr error
				if changes, cerr = st.Book.Overrule(st.Attempt, d, o.Proof); cerr != nil {
					return Outcome{}, true, cerr
				}
			case Unsettleable:
				unsettled = append(unsettled, d.ID)
				st.Rejected = append(st.Rejected, Rejection{d, o.Reason})
			default:
				st.Rejected = append(st.Rejected, Rejection{d, o.Reason})
			}
		}
		st.Disputes = st.Disputes[1:]
		if err := recorded(st, steps, changes); err != nil {
			return Outcome{}, true, err
		}
	}
	if len(unsettled) == 0 {
		return Outcome{}, false, nil
	}
	// The operator rules, and the next attempt starts: the ladder is not run again for these disputes.
	o, _ := escalate(st, ReasonDisputed, disputeDetail(st, unsettled))
	st.Attempt, st.Phase, st.Unreadable = st.Attempt+1, PhaseImplement, nil
	return o, true, steps.Save(st)
}

// disputedBlocking lists the ids of the open blocking findings the implementer disputed in the latest
// attempt that disputed, without success.
func disputedBlocking(st *State) []string {
	var ids []string
	for _, f := range st.Book.Blocking() {
		if slices.ContainsFunc(st.Rejected, func(r Rejection) bool { return r.ID == f.ID }) {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// disputeDetail shows both sides of each dispute: the reviewer's claim and location, and the implementer's
// evidence, with why no check settled it.
func disputeDetail(st *State, ids []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "the implementer disputed %d finding(s) that no check settled", len(ids))
	for _, id := range ids {
		f := st.Book.Records[id].Finding
		fmt.Fprintf(&b, "\n- %s (%s, from %s) at %s\n  reviewer's claim: %s", f.ID, f.Severity, f.Source, f.Location, strings.TrimSpace(f.Claim))
		for _, r := range st.Rejected {
			if r.ID == id {
				fmt.Fprintf(&b, "\n  implementer's evidence (check %s, test %s): %s\n  why it was not settled: %s", r.Check, r.Test, strings.TrimSpace(r.Evidence), r.Reason)
			}
		}
	}
	return b.String()
}
