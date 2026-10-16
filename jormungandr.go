// Package jormungandr runs Yggdrasil's bounded loop (spec §2.1, §4.2, §4.6, §7.3, §12): an
// implementer works, a ladder of checks and read-only reviewers judges the work, the findings that are
// still open go back to the next attempt, and after a fixed number of attempts the loop escalates. It
// also holds the retry policy for a session that fails for a reason that is not the agent's. It does
// none of the work itself: Odin gives it the steps (a session, a check, a ledger line), and it decides
// what comes next. It depends on Gungnir alone.
package jormungandr

import (
	"errors"
	"fmt"

	"github.com/ouroboric-dev/gungnir"
)

// QuotaError is a provider quota or credit balance that is used up. The run pauses, resumable, and
// says why (spec §12).
type QuotaError struct{ Reason string }

func (e *QuotaError) Error() string { return "the provider's quota is used up: " + e.Reason }

// ExhaustedError is a transient failure that outlasted its retries. Nothing in the work is wrong: the
// run stops failed, and a resume tries again. It is not a loop attempt (spec §12).
type ExhaustedError struct {
	Tries int
	Last  string // why the last try failed
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("the session failed %d times in a row for a reason that is not the agent's, the last time with: %s", e.Tries, e.Last)
}

// AgentFailure is a session that failed as an agent does: a non-zero exit, a timeout, a turn limit,
// or output that can't be used. It counts as a loop attempt (spec §12).
type AgentFailure struct{ Reason string }

func (e *AgentFailure) Error() string { return "the agent failed: " + e.Reason }

// Classify says how the end of one session went. kind is empty for a session that succeeded; otherwise
// it is how the session failed, and why is what it said. A failed result with no kind failed as an
// agent does, as does any error that is not a *gungnir.SessionError: the safe reading of an unknown
// failure is the one that counts against the loop's attempts.
func Classify(res gungnir.Result, err error) (kind gungnir.FailureKind, why string) {
	switch {
	case err != nil:
		return gungnir.FailureOf(err), err.Error()
	case res.Status == gungnir.StatusFailed:
		kind = res.Failure
		if kind == "" {
			kind = gungnir.FailureAgent
		}
		return kind, res.Summary
	}
	return "", ""
}

// IsCap reports whether err is a budget cap that is reached, and returns it.
func IsCap(err error) (*gungnir.CapError, bool) {
	var c *gungnir.CapError
	return c, errors.As(err, &c)
}
