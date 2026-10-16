package jormungandr

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ouroboric-dev/gungnir"
)

func TestClassifyReadsTheEndOfASession(t *testing.T) {
	cases := []struct {
		name string
		res  gungnir.Result
		err  error
		want gungnir.FailureKind
		why  string
	}{
		{"a success", gungnir.Result{Status: gungnir.StatusOK, Summary: "done"}, nil, "", ""},
		{"a failed result with no kind", gungnir.Result{Status: gungnir.StatusFailed, Summary: "tests fail"}, nil, gungnir.FailureAgent, "tests fail"},
		{"a transient result", gungnir.Result{Status: gungnir.StatusFailed, Summary: "429", Failure: gungnir.FailureTransient}, nil, gungnir.FailureTransient, "429"},
		{"a quota result", gungnir.Result{Status: gungnir.StatusFailed, Summary: "no credits", Failure: gungnir.FailureQuota}, nil, gungnir.FailureQuota, "no credits"},
		{"a plain error", gungnir.Result{}, errors.New("exit 1"), gungnir.FailureAgent, "exit 1"},
		{"an infra error", gungnir.Result{}, fmt.Errorf("wrapped: %w", &gungnir.SessionError{Kind: gungnir.FailureInfra, Err: errors.New("podman")}), gungnir.FailureInfra, "wrapped: podman"},
		{"a transient error", gungnir.Result{}, &gungnir.SessionError{Kind: gungnir.FailureTransient, Err: errors.New("crash at start")}, gungnir.FailureTransient, "crash at start"},
	}
	for _, c := range cases {
		kind, why := Classify(c.res, c.err)
		if kind != c.want || why != c.why {
			t.Errorf("%s: Classify() = %q, %q; want %q, %q", c.name, kind, why, c.want, c.why)
		}
	}
}

func TestTheOutcomesSayWhatHappened(t *testing.T) {
	for err, want := range map[error]string{
		&QuotaError{Reason: "credits used up"}:         "quota is used up: credits used up",
		&ExhaustedError{Tries: 4, Last: "HTTP 429"}:    "4 times in a row",
		&AgentFailure{Reason: "timed out after 20m0s"}: "the agent failed: timed out",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%T: Error() = %q, want it to contain %q", err, err.Error(), want)
		}
	}
	c := &gungnir.CapError{Scope: gungnir.CapRun, Dimension: gungnir.CapTurns, Used: 10, Limit: 10}
	if got, ok := IsCap(fmt.Errorf("wrapped: %w", c)); !ok || got != c {
		t.Errorf("IsCap() = %v, %v", got, ok)
	}
	if _, ok := IsCap(errors.New("x")); ok {
		t.Error("IsCap() = true for a plain error")
	}
}
