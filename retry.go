package jormungandr

import (
	"context"
	"time"

	"github.com/ouroboric-dev/gungnir"
)

// Backoff is the retry policy for a transient failure (spec §12): a session that fails for a reason
// that is not the agent's runs again after a growing wait, up to Retries times.
type Backoff struct {
	Base    time.Duration // the wait before the first retry
	Max     time.Duration // no wait is longer than this
	Retries int           // how many times a session is retried; a session therefore runs at most Retries+1 times
}

// DefaultBackoff is 5 seconds, then 10, then 20, for three retries (spec §12).
var DefaultBackoff = Backoff{Base: 5 * time.Second, Max: time.Minute, Retries: 3}

// Delay returns the wait before retry number n, counting from 1: Base, twice Base, four times Base,
// and so on, never more than Max.
func (b Backoff) Delay(n int) time.Duration {
	d := b.Base
	for i := 1; i < n && d < b.Max; i++ {
		d *= 2
	}
	return min(d, b.Max)
}

// Retrier runs one session and decides what its failure means.
type Retrier struct {
	Backoff Backoff
	// Meter, when set, is asked before every try, the first and each retry: a session never starts
	// over a budget cap (spec §7.4).
	Meter gungnir.Meter
	// Sleep waits d, or until ctx ends. It is time-based unless a test replaces it.
	Sleep func(ctx context.Context, d time.Duration) error
	// OnRetry is told about each retry before the wait: the failed try's number, the wait and why.
	OnRetry func(try int, wait time.Duration, why string) error
}

// Try runs the session once; n counts from 1. It must count what the session spent, even when it
// fails: the caps see only what the caller adds up.
type Try func(ctx context.Context, n int) (gungnir.Result, error)

// Do runs try until it succeeds or fails in a way a retry can't help. It returns:
//   - the result and nil, for a session that succeeded;
//   - a *gungnir.CapError, when a budget cap is reached before a try;
//   - a *QuotaError, when the provider's quota is used up: the run pauses;
//   - an *ExhaustedError, when transient failures outlast the retries;
//   - an *AgentFailure, when the session failed as an agent does (spec §12: a loop attempt);
//   - the session's own error, for an infrastructure failure: the run stops, with no retry loop;
//   - the context's error, when ctx ends.
func (r Retrier) Do(ctx context.Context, try Try) (gungnir.Result, error) {
	sleep := r.Sleep
	if sleep == nil {
		sleep = wait
	}
	for n := 1; ; n++ {
		if r.Meter != nil {
			if hit := r.Meter.Check(); hit != nil {
				return gungnir.Result{}, hit
			}
		}
		res, err := try(ctx, n)
		kind, why := Classify(res, err)
		switch kind {
		case "":
			return res, nil
		case gungnir.FailureQuota:
			return res, &QuotaError{Reason: why}
		case gungnir.FailureInfra:
			return res, err
		case gungnir.FailureTransient:
			if n > r.Backoff.Retries {
				return res, &ExhaustedError{Tries: n, Last: why}
			}
			d := r.Backoff.Delay(n)
			if r.OnRetry != nil {
				if err := r.OnRetry(n, d, why); err != nil {
					return res, err
				}
			}
			if err := sleep(ctx, d); err != nil {
				return res, err
			}
		default:
			if ctx.Err() != nil {
				return res, ctx.Err() // the operator stopped the run; it is no failure of the agent's
			}
			return res, &AgentFailure{Reason: why}
		}
	}
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
