package jormungandr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ouroboric-dev/gungnir"
)

func TestBackoffDoublesUpToItsMaximum(t *testing.T) {
	b := Backoff{Base: 5 * time.Second, Max: 12 * time.Second, Retries: 3}
	for n, want := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 12 * time.Second, 9: 12 * time.Second} {
		if got := b.Delay(n); got != want {
			t.Errorf("Delay(%d) = %v, want %v", n, got, want)
		}
	}
	if DefaultBackoff.Retries != 3 || DefaultBackoff.Delay(1) != 5*time.Second || DefaultBackoff.Delay(3) != 20*time.Second {
		t.Errorf("DefaultBackoff = %+v", DefaultBackoff)
	}
}

type script struct {
	steps []func() (gungnir.Result, error)
	calls int
}

func (s *script) try(_ context.Context, n int) (gungnir.Result, error) {
	s.calls++
	if n != s.calls {
		panic("the try number must count from 1")
	}
	return s.steps[min(n, len(s.steps))-1]()
}

func ok() (gungnir.Result, error) { return gungnir.Result{Status: gungnir.StatusOK}, nil }
func transient() (gungnir.Result, error) {
	return gungnir.Result{}, &gungnir.SessionError{Kind: gungnir.FailureTransient, Err: errors.New("HTTP 429")}
}

type slept struct{ waits []time.Duration }

func (s *slept) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

func retrier(s *slept) Retrier {
	return Retrier{Backoff: Backoff{Base: time.Second, Max: time.Minute, Retries: 3}, Sleep: s.sleep}
}

func TestATransientFailureIsRetriedWithABackoffAndIsNotAnAttempt(t *testing.T) {
	sl := &slept{}
	sc := &script{steps: []func() (gungnir.Result, error){transient, transient, ok}}
	var told []string
	r := retrier(sl)
	r.OnRetry = func(try int, wait time.Duration, why string) error { told = append(told, why); return nil }
	res, err := r.Do(context.Background(), sc.try)
	if err != nil || res.Status != gungnir.StatusOK || sc.calls != 3 {
		t.Fatalf("Do() = %+v, %v after %d tries; want a success on the third", res, err, sc.calls)
	}
	if len(sl.waits) != 2 || sl.waits[0] != time.Second || sl.waits[1] != 2*time.Second || len(told) != 2 || told[0] != "HTTP 429" {
		t.Errorf("waits %v, told %v", sl.waits, told)
	}
}

func TestTransientFailuresStopAfterTheRetriesAreUsed(t *testing.T) {
	sl := &slept{}
	sc := &script{steps: []func() (gungnir.Result, error){transient}}
	_, err := retrier(sl).Do(context.Background(), sc.try)
	var ex *ExhaustedError
	if !errors.As(err, &ex) || ex.Tries != 4 || sc.calls != 4 || len(sl.waits) != 3 {
		t.Errorf("Do() = %v after %d tries and %d waits; want an ExhaustedError after 4 tries and 3 waits", err, sc.calls, len(sl.waits))
	}
}

func TestOtherFailuresAreNotRetried(t *testing.T) {
	agentErr := func() (gungnir.Result, error) { return gungnir.Result{}, errors.New("exit 1") }
	agentRes := func() (gungnir.Result, error) {
		return gungnir.Result{Status: gungnir.StatusFailed, Summary: "tests fail"}, nil
	}
	quota := func() (gungnir.Result, error) {
		return gungnir.Result{Status: gungnir.StatusFailed, Summary: "no credits", Failure: gungnir.FailureQuota}, nil
	}
	infra := func() (gungnir.Result, error) {
		return gungnir.Result{}, &gungnir.SessionError{Kind: gungnir.FailureInfra, Err: errors.New("podman is gone")}
	}
	for name, c := range map[string]struct {
		step func() (gungnir.Result, error)
		is   func(error) bool
	}{
		"an agent error":  {agentErr, func(e error) bool { var a *AgentFailure; return errors.As(e, &a) && a.Reason == "exit 1" }},
		"a failed result": {agentRes, func(e error) bool { var a *AgentFailure; return errors.As(e, &a) && a.Reason == "tests fail" }},
		"a used-up quota": {quota, func(e error) bool { var q *QuotaError; return errors.As(e, &q) && q.Reason == "no credits" }},
		"an infra error": {infra, func(e error) bool {
			var se *gungnir.SessionError
			return errors.As(e, &se) && se.Kind == gungnir.FailureInfra
		}},
	} {
		sl := &slept{}
		sc := &script{steps: []func() (gungnir.Result, error){c.step}}
		_, err := retrier(sl).Do(context.Background(), sc.try)
		if !c.is(err) || sc.calls != 1 || len(sl.waits) != 0 {
			t.Errorf("%s: Do() = %v after %d tries and %d waits; want one try and no wait", name, err, sc.calls, len(sl.waits))
		}
	}
}

type meter struct{ hit *gungnir.CapError }

func (m *meter) Check() *gungnir.CapError { return m.hit }

func TestNoTryStartsOverABudgetCap(t *testing.T) {
	cap := &gungnir.CapError{Scope: gungnir.CapRun, Dimension: gungnir.CapTokens, Used: 10, Limit: 10}
	m := &meter{hit: cap}
	sc := &script{steps: []func() (gungnir.Result, error){ok}}
	r := retrier(&slept{})
	r.Meter = m
	if _, err := r.Do(context.Background(), sc.try); err != error(cap) || sc.calls != 0 {
		t.Errorf("Do() = %v after %d tries; want the cap, before any try", err, sc.calls)
	}
	// The first try spends the last of the budget and fails transiently: the retry must not start.
	m.hit = nil
	sc = &script{steps: []func() (gungnir.Result, error){func() (gungnir.Result, error) { m.hit = cap; return transient() }, ok}}
	if _, err := r.Do(context.Background(), sc.try); err != error(cap) || sc.calls != 1 {
		t.Errorf("Do() = %v after %d tries; want the cap before the retry", err, sc.calls)
	}
}

func TestACancelledRunIsNotAnAgentFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sc := &script{steps: []func() (gungnir.Result, error){func() (gungnir.Result, error) {
		cancel()
		return gungnir.Result{}, context.Canceled
	}}}
	if _, err := retrier(&slept{}).Do(ctx, sc.try); !errors.Is(err, context.Canceled) {
		var a *AgentFailure
		t.Errorf("Do() = %v (agent failure %v); want the context's error", err, errors.As(err, &a))
	}
	// The wait between retries ends with the context too, and sleeps for real by default.
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := Retrier{Backoff: Backoff{Base: time.Hour, Max: time.Hour, Retries: 3}}
	began := time.Now()
	if _, err := r.Do(ctx, (&script{steps: []func() (gungnir.Result, error){transient}}).try); !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > 5*time.Second {
		t.Errorf("Do() = %v after %v; want the deadline, promptly", err, time.Since(began))
	}
}

func TestAFailingOnRetryHookStopsTheRetries(t *testing.T) {
	r := retrier(&slept{})
	r.OnRetry = func(int, time.Duration, string) error { return errors.New("the ledger is full") }
	sc := &script{steps: []func() (gungnir.Result, error){transient, ok}}
	if _, err := r.Do(context.Background(), sc.try); err == nil || sc.calls != 1 {
		t.Errorf("Do() = %v after %d tries; a retry that can't be recorded must not happen", err, sc.calls)
	}
}
