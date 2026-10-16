package jormungandr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ouroboric-dev/gungnir"
)

// Phase is where a loop is within an attempt.
type Phase string

const (
	PhaseImplement Phase = "implement" // the attempt's implementer session has not finished
	PhaseLadder    Phase = "ladder"    // the implementer finished: the checks and reviewers judge its work
)

// Config is what a loop is made of (spec §4.2): a bound on its attempts, and the ladder of checks that
// judges each one, in order, fast to slow (spec §9.5).
type Config struct {
	Attempts int      // the most implementer attempts before the loop escalates; 3 by default in a graph
	Checks   []string // check names, in order
}

// State is everything a loop must remember to go on after it stops for a budget cap, a quota or a
// restart. Odin keeps it in run.json and saves it at every boundary, so a resume never repeats an
// attempt or loses a finding.
type State struct {
	Attempt int    `json:"attempt"`           // the attempt in progress, from 1
	Limit   int    `json:"limit"`             // the last attempt before the loop escalates
	Phase   Phase  `json:"phase"`             // where the attempt stands
	Passed  bool   `json:"passed,omitempty"`  // the loop finished: the work passed its ladder
	Book    Book   `json:"book"`              // the findings, open and closed
	Failure string `json:"failure,omitempty"` // how the last implementer session failed, for the next attempt
}

// NewState starts a loop at attempt 1.
func NewState(cfg Config) *State {
	return &State{Attempt: 1, Limit: cfg.Attempts, Phase: PhaseImplement}
}

// Input is what an implementer attempt needs besides its prompt.
type Input struct {
	Open        []gungnir.Finding // the findings to fix
	LastFailure string            // how the last implementer session ended, when it failed
}

// CheckOutcome is how one check ended on this attempt.
type CheckOutcome struct {
	Passed    bool
	FindingID string          // the id of the finding this check raises when it fails
	Finding   gungnir.Finding // when it failed: the finding
	Proof     string          // when it passed: the evidence that it ran and passed, which closes its finding
}

// Steps are the side effects of a loop, which Odin implements. A session that failed as an agent does
// returns an *AgentFailure; any other error is one the loop passes on.
type Steps interface {
	// Implement runs one implementer session, fresh, and commits what it did. The attempt counts from 1.
	Implement(ctx context.Context, attempt int, in Input) error
	// Check runs one check of the ladder on the committed work.
	Check(ctx context.Context, attempt int, name string) (CheckOutcome, error)
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
)

// Reasons a loop escalates.
const ReasonExhausted = "attempts-exhausted"

// Outcome is how a run of the loop ended.
type Outcome struct {
	Kind     OutcomeKind
	Attempts int // the attempt the loop ended in
	// Escalated: why, in words, and the findings still open.
	Reason string
	Detail string
	Open   []gungnir.Finding
}

// Run runs the loop from where st stands until it passes or escalates. It changes st and saves it at
// every boundary. Any other error ends the run of the loop as it is: an infrastructure failure,
// transient failures that outlasted their retries, a hard stop of Odin's, a cancelled context.
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
				return escalate(st, ReasonExhausted, exhausted(st))
			}
			if err := steps.Save(st); err != nil {
				return Outcome{}, err
			}
			err := steps.Implement(ctx, st.Attempt, Input{Open: st.Book.Open(), LastFailure: st.Failure})
			var af *AgentFailure
			switch {
			case err == nil:
				st.Failure, st.Phase = "", PhaseLadder
				if err := steps.Save(st); err != nil {
					return Outcome{}, err
				}
			case errors.As(err, &af):
				// A session that failed as an agent does is an attempt used (spec §12).
				st.Failure = af.Reason
				st.Attempt++
				continue
			default:
				return Outcome{}, err
			}
		}
		out, done, err := ladder(ctx, cfg, st, steps)
		if err != nil || done {
			return out, err
		}
		st.Attempt, st.Phase = st.Attempt+1, PhaseImplement
	}
}

// ladder judges the attempt's work. done is true when the loop ends here: it passed, escalated or
// must stop. Otherwise the work failed the ladder, and the next attempt starts.
func ladder(ctx context.Context, cfg Config, st *State, steps Steps) (out Outcome, done bool, err error) {
	var passed []string
	clear := true
	for _, name := range cfg.Checks {
		co, err := steps.Check(ctx, st.Attempt, name)
		if err != nil {
			return Outcome{}, true, err
		}
		var changes []Change
		if co.Passed {
			passed = append(passed, name)
			changes = st.Book.CheckPassed(st.Attempt, co.FindingID, co.Proof)
		} else {
			clear = false
			changes = st.Book.CheckFailed(st.Attempt, co.Finding)
		}
		// Saved with each rung, so a stop in the middle of the ladder repeats no line of the ledger.
		if err := recorded(st, steps, changes); err != nil {
			return Outcome{}, true, err
		}
		if !co.Passed {
			break // the ladder stops at the first rung that fails: the slower ones wait
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
