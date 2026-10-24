package jormungandr

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ouroboric-dev/gungnir"
)

// Verdict is how a dispute was decided.
type Verdict string

const (
	// Holds: the named check ran and passed in this attempt, and its output names the test.
	Holds Verdict = "holds"
	// DoesNotHold: the check did not run, did not pass, or did not print the test. The finding stays open.
	DoesNotHold Verdict = "does-not-hold"
	// Unsettleable: no check of the loop can decide it. The loop escalates as disputed.
	Unsettleable Verdict = "unsettleable"
)

// DisputeOutcome is what Steps.Dispute answers for one dispute.
type DisputeOutcome struct {
	Verdict Verdict
	Proof   string // Holds: the check run, which goes into the finding's proof
	Reason  string // DoesNotHold and Unsettleable: why
}

// Rejection is a dispute that did not hold, with the reason, kept so the next attempt is told and the
// escalation can show both sides.
type Rejection struct {
	gungnir.Dispute
	Reason string `json:"reason"`
}

// settleDisputes decides the attempt's disputes, one by one, after the checks ran and before the
// reviewers. A dispute that holds overrules its finding; one that doesn't is remembered; one that can't be
// used is recorded and ignored. When any dispute is unsettleable the loop escalates, after the rest were
// decided. stop is true when the loop ends here.
func settleDisputes(ctx context.Context, cfg Config, st *State, steps Steps) (out Outcome, stop bool, err error) {
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
		case !slices.Contains(cfg.Checks, d.Check):
			unsettled = append(unsettled, d.ID)
			st.Rejected = append(st.Rejected, Rejection{d, fmt.Sprintf("check %q is not one of this loop's checks (%s), so no check can settle it", d.Check, strings.Join(cfg.Checks, ", "))})
		default:
			o, err := steps.Dispute(ctx, st.Attempt, d)
			if err != nil {
				return Outcome{}, true, err
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
			default: // DoesNotHold, and any verdict the loop does not know: the safe reading is that it did not hold
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
