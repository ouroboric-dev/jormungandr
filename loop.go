package jormungandr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ouroboric-dev/gungnir"
	"github.com/ouroboric-dev/gungnir/schema"
)

// Phase is where a loop is within an attempt.
type Phase string

const (
	PhaseImplement Phase = "implement" // the attempt's implementer session has not finished
	PhaseLadder    Phase = "ladder"    // the implementer finished: the checks and reviewers judge its work
)

// Config is what a loop is made of (spec §4.2): a bound on its attempts, and the ladder of checks and
// reviewers that judge each one, in order, fast to slow (spec §9.5).
type Config struct {
	Attempts  int      // the most implementer attempts before the loop escalates; 3 by default in a graph
	Checks    []string // check names, in order
	Reviewers []string // reviewer ids, in order; they run only when every check passed
}

// State is everything a loop must remember to go on after it stops for a budget cap, a quota or a
// restart. Odin keeps it in run.json and saves it at every boundary, so a resume never repeats an
// attempt or loses a finding.
type State struct {
	Attempt    int            `json:"attempt"`              // the attempt in progress, from 1
	Limit      int            `json:"limit"`                // the last attempt before the loop escalates
	Phase      Phase          `json:"phase"`                // where the attempt stands
	Passed     bool           `json:"passed,omitempty"`     // the loop finished: the work passed its ladder
	Book       Book           `json:"book"`                 // the findings, open and closed
	Failure    string         `json:"failure,omitempty"`    // how the last implementer session failed, for the next attempt
	Unreadable map[string]int `json:"unreadable,omitempty"` // reviewer id: unreadable answers in a row
	// Disputes are the implementer's disputes of this attempt that the ladder has not decided yet. Rejected
	// are those that did not hold in the latest attempt that disputed: the next attempt is told, and a loop
	// that runs out of attempts with one of them still open escalates as disputed.
	Disputes []gungnir.Dispute `json:"disputes,omitempty"`
	Rejected []Rejection       `json:"rejected,omitempty"`
}

// NewState starts a loop at attempt 1.
func NewState(cfg Config) *State {
	return &State{Attempt: 1, Limit: cfg.Attempts, Phase: PhaseImplement}
}

// Extend gives an escalated loop another full set of attempts, after an operator sent the work back
// (spec §7.3). The findings stay, and a reviewer that was unreadable is asked again.
func (s *State) Extend(cfg Config) {
	s.Limit = max(s.Limit, s.Attempt+cfg.Attempts-1)
	s.Unreadable = nil
}

// Reopen sends a loop back for another set of attempts after something downstream refused its work, such
// as the merge gate (spec §4.5). A loop that passed starts a new attempt, with attempts up to the full
// set again; one that has not finished is extended as an operator's rejection extends it. The findings
// stay, open or closed, as they were.
func (s *State) Reopen(cfg Config) {
	if !s.Passed {
		s.Extend(cfg)
		return
	}
	s.Passed = false
	s.Attempt++
	s.Phase, s.Failure, s.Unreadable = PhaseImplement, "", nil
	s.Limit = max(s.Limit, s.Attempt+cfg.Attempts-1)
}

// Input is what an implementer attempt needs besides its prompt.
type Input struct {
	Open        []gungnir.Finding // the findings to fix
	Rejected    []Rejection       // the implementer's disputes that did not hold last attempt, and why
	LastFailure string            // how the last implementer session ended, when it failed
}

// CheckOutcome is how one check ended on this attempt.
type CheckOutcome struct {
	Passed bool
	Check  string // the check's name
	// Findings are what a failed check reports, one per failure (ygg.check/v1, read by gleipnir): each has the
	// id check-<name>-<12 hex> and the check as its source. A check that ran and failed with no typed result
	// reports one, check-<name>-result.
	Findings []gungnir.Finding
	Proof    string   // when it passed: the evidence that it ran and passed, which closes its findings
	Failed   []string // the ids of the tests the typed result says failed
	// PassedTests are the ids of the tests the typed result says ran and passed (ygg.check/v1 passed_tests). A
	// dispute holds only on a test listed here.
	PassedTests []string
}

// Steps are the side effects of a loop, which Odin implements. A step that stops the loop for a budget
// cap or a quota returns a *gungnir.CapError or a *QuotaError; a session that failed as an agent does
// returns an *AgentFailure; anything else is an error the loop passes on.
type Steps interface {
	// Implement runs one implementer session, fresh, and commits what it did. The attempt counts from 1. It
	// returns the implementer's answer, read and decoded as a ygg.implement/v1 document: one that can't be
	// read is an *AgentFailure.
	Implement(ctx context.Context, attempt int, in Input) (schema.Implement, error)
	// Check runs one check of the ladder on the committed work.
	Check(ctx context.Context, attempt int, name string) (CheckOutcome, error)
	// Review runs one read-only reviewer and returns its report, read and decoded as a ygg.review/v1
	// document: an answer that can't be read is an *UnreadableError. open is what it reported before and
	// still holds open; passed lists the checks that passed on this attempt.
	Review(ctx context.Context, attempt int, reviewer string, open []gungnir.Finding, passed []string) (schema.Review, error)
	// Record writes the changes to the findings to the ledger.
	Record(attempt int, changes []Change) error
	// Save keeps the loop's state, durably, before the loop goes on.
	Save(s *State) error
}

// OutcomeKind says how a run of the loop ended.
type OutcomeKind string

const (
	Passed    OutcomeKind = "passed"    // no blocking finding is open: the work may go on
	Escalated OutcomeKind = "escalated" // the loop gave up: a person decides (spec §7.3)
	Capped    OutcomeKind = "capped"    // a budget cap is reached: the run stops at budget-cap, resumable
	Paused    OutcomeKind = "paused"    // the provider's quota is used up: the run pauses, resumable
)

// Reasons a loop escalates.
const (
	ReasonExhausted  = "attempts-exhausted"
	ReasonDisputed   = "finding-disputed" // the implementer disputed a finding that no check settled
	ReasonUnreadable = "reviewer-unreadable"
	ReasonBlocked    = "implementer-blocked" // the implementer answered that it could not do the work: a person decides
)

// Outcome is how a run of the loop ended.
type Outcome struct {
	Kind     OutcomeKind
	Attempts int // the attempt the loop ended in
	// Escalated: why, in words, and the findings still open.
	Reason string
	Detail string
	Open   []gungnir.Finding
	// Capped: the cap. Paused: the quota.
	Cap   *gungnir.CapError
	Quota *QuotaError
}

// Run runs the loop from where st stands until it passes, escalates, or must stop. It changes st and
// saves it at every boundary. Any other error ends the run of the loop as it is: an infrastructure
// failure, transient failures that outlasted their retries, a hard stop of Odin's, a cancelled context.
func Run(ctx context.Context, cfg Config, st *State, steps Steps) (Outcome, error) {
	if st.Passed {
		return Outcome{Kind: Passed, Attempts: st.Attempt}, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		if st.Phase == PhaseImplement {
			if st.Attempt > st.Limit {
				if ids := disputedBlocking(st); len(ids) > 0 {
					return escalate(st, ReasonDisputed, disputeDetail(st, ids))
				}
				return escalate(st, ReasonExhausted, exhausted(st))
			}
			if err := steps.Save(st); err != nil {
				return Outcome{}, err
			}
			ans, err := steps.Implement(ctx, st.Attempt, Input{Open: st.Book.Open(), Rejected: st.Rejected, LastFailure: st.Failure})
			var af *AgentFailure
			switch {
			case err == nil && ans.Blocked != nil:
				// The attempt ends here: no check or reviewer judges work its author says it could not do. A
				// person decides; if they send it back, the next attempt starts and is told why.
				o, _ := escalate(st, ReasonBlocked, ans.Blocked.Reason)
				st.Failure, st.Disputes, st.Rejected = "you answered that you were blocked: "+ans.Blocked.Reason, nil, nil
				st.Attempt, st.Phase, st.Unreadable = st.Attempt+1, PhaseImplement, nil
				return o, steps.Save(st)
			case err == nil:
				// What this attempt disputes replaces what the last one did.
				st.Failure, st.Phase, st.Disputes, st.Rejected = "", PhaseLadder, ans.Disputes, nil
				if err := steps.Save(st); err != nil {
					return Outcome{}, err
				}
			case errors.As(err, &af):
				// A session that failed as an agent does is an attempt used (spec §12).
				st.Failure = af.Reason
				st.Attempt++
				continue
			default:
				return stopped(st, err)
			}
		}
		out, done, err := ladder(ctx, cfg, st, steps)
		if err != nil || done {
			return out, err
		}
		st.Attempt, st.Phase, st.Unreadable = st.Attempt+1, PhaseImplement, nil
	}
}

// ladder judges the attempt's work. done is true when the loop ends here: it passed, escalated or
// must stop. Otherwise the work failed the ladder, and the next attempt starts.
func ladder(ctx context.Context, cfg Config, st *State, steps Steps) (out Outcome, done bool, err error) {
	var passed []string
	ran := map[string]CheckOutcome{} // this attempt's outcomes, by check: the disputes are settled on them
	clear := true
	for _, name := range cfg.Checks {
		co, err := steps.Check(ctx, st.Attempt, name)
		if err != nil {
			return Outcome{}, true, err
		}
		ran[name] = co
		var changes []Change
		var cerr error
		if co.Passed {
			passed = append(passed, name)
			changes, cerr = st.Book.CheckPassed(st.Attempt, name, co.Proof)
		} else {
			clear = false
			changes, cerr = st.Book.CheckFailed(st.Attempt, name, co.Findings)
		}
		if cerr != nil {
			return Outcome{}, true, cerr
		}
		// Saved with each rung, so a stop in the middle of the ladder repeats no line of the ledger.
		if err := recorded(st, steps, changes); err != nil {
			return Outcome{}, true, err
		}
		if !co.Passed {
			break // the ladder stops at the first rung that fails: the slower ones wait
		}
	}
	if out, stop, err := settleDisputes(cfg, st, steps, ran); err != nil || stop {
		return out, true, err
	}
	for _, rv := range cfg.Reviewers {
		if !clear {
			break
		}
		out, stop, err := review(ctx, st, steps, rv, passed)
		if err != nil || stop {
			return out, true, err
		}
		if blockingFrom(st, rv) {
			clear = false
		}
	}
	if clear && len(st.Book.Blocking()) == 0 {
		st.Passed = true
		if err := steps.Save(st); err != nil {
			return Outcome{}, true, err
		}
		return Outcome{Kind: Passed, Attempts: st.Attempt}, true, nil
	}
	return Outcome{}, false, nil
}

// recorded writes changes to the ledger, then saves the state that holds them.
func recorded(st *State, steps Steps, changes []Change) error {
	if err := steps.Record(st.Attempt, changes); err != nil {
		return err
	}
	return steps.Save(st)
}

func blockingFrom(st *State, reviewer string) bool {
	for _, f := range st.Book.Blocking() {
		if f.Source == reviewer {
			return true
		}
	}
	return false
}

// review asks one reviewer, and asks it once more if its answer can't be used: an answer that can't be
// read, one that says it is blocked, one that drops a finding, one that closes a finding without proof, or a session that failed.
// Neither runs the implementer again. Twice in a row, the loop escalates (spec §4.6). stop is true when
// the loop ends here.
func review(ctx context.Context, st *State, steps Steps, rv string, passed []string) (out Outcome, stop bool, err error) {
	for {
		report, err := steps.Review(ctx, st.Attempt, rv, st.Book.OpenFrom(rv), passed)
		var af *AgentFailure
		var ue *UnreadableError
		var why error
		switch {
		case err == nil && report.Blocked != nil:
			// A reviewer that says it could not do its work gave no report: it is asked once more, then a person decides.
			why = fmt.Errorf("blocked: %s", report.Blocked.Reason)
		case err == nil:
			changes, rerr := st.Book.Review(st.Attempt, rv, passed, gungnir.Report{Findings: report.Findings})
			if rerr == nil {
				delete(st.Unreadable, rv)
				return Outcome{}, false, recorded(st, steps, changes)
			}
			why = rerr
		case errors.As(err, &ue):
			why = ue
		case errors.As(err, &af):
			why = af
		default:
			o, err := stopped(st, err)
			return o, true, err
		}
		if st.Unreadable == nil {
			st.Unreadable = map[string]int{}
		}
		st.Unreadable[rv]++
		if err := steps.Save(st); err != nil {
			return Outcome{}, true, err
		}
		if st.Unreadable[rv] >= 2 {
			o, err := escalate(st, ReasonUnreadable, fmt.Sprintf("reviewer %s gave an answer that can't be used twice in a row; the last time: %v", rv, why))
			return o, true, err
		}
	}
}

// stopped turns a budget cap or a used-up quota into the loop's outcome, with the state saved; any
// other error is the caller's.
func stopped(st *State, err error) (Outcome, error) {
	var q *QuotaError
	switch c, isCap := IsCap(err); {
	case isCap:
		return Outcome{Kind: Capped, Attempts: st.Attempt, Cap: c}, nil
	case errors.As(err, &q):
		return Outcome{Kind: Paused, Attempts: st.Attempt, Quota: q}, nil
	}
	return Outcome{}, err
}

func escalate(st *State, reason, detail string) (Outcome, error) {
	return Outcome{Kind: Escalated, Attempts: min(st.Attempt, st.Limit), Reason: reason, Detail: detail, Open: st.Book.Open()}, nil
}

func exhausted(st *State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d attempts ended with %d blocking finding(s) open", st.Limit, len(st.Book.Blocking()))
	if st.Failure != "" {
		fmt.Fprintf(&b, "; the last session failed: %s", st.Failure)
	}
	return b.String()
}
