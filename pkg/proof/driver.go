package proof

import (
	"context"
	"time"
)

// This file is the part of the harness that decides what to do about a run
// that did not go well. It exists because that judgement was made by hand,
// badly, during a suite run on 2026-08-19: a driver treated "Prometheus is not
// answering" as a verdict on six scenarios and burned all six in two seconds.
// The rules below are the corrections, written down so they are testable
// rather than remembered.

// Retryable reports whether a result deserves another attempt.
//
// The rule is narrow on purpose: only a blind run is retried. A blind run is
// one where the harness could not see the cluster, so it says nothing about
// the entry and re-running it costs nothing but time. A run where the observer
// was healthy and the entry simply did not fire is a finding, and retrying a
// finding until it goes away is how a harness launders a bug into a pass.
//
// This is the same line the detector draws between `blind` and `clear`, and
// the harness has no business drawing it anywhere else.
func Retryable(res Result) bool {
	return res.Blind
}

// Observer is the narrow view of Prometheus that WaitForObserver needs. The
// Runner's client satisfies it; a test can satisfy it with a counter.
type Observer interface {
	Query(ctx context.Context, q string) (float64, error)
}

// WaitForObserver blocks until Prometheus answers or the budget runs out,
// reporting how long it waited.
//
// A suite that runs for an hour outlives things that were up when it started.
// A port-forward drops, a pod restarts, a laptop's network changes. The old
// behaviour was to ask once and, on a refusal, mark the item failed and move
// to the next one, which converts a transient gap into a row of verdicts about
// entries nobody looked at. Waiting is not leniency: nothing is injected until
// the observer is back, so the alternative to waiting is not a stricter
// measurement, it is no measurement at all.
//
// It returns false only when the budget is exhausted or the context ends,
// which is the case where refusing really is the right answer.
func WaitForObserver(ctx context.Context, obs Observer, budget, poll time.Duration, now func() time.Time, sleep func(context.Context, time.Duration), log func(string, ...any)) (waited time.Duration, ok bool) {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	if poll <= 0 {
		poll = 5 * time.Second
	}

	start := now()
	deadline := start.Add(budget)
	announced := false
	for {
		if _, err := obs.Query(ctx, "vector(1)"); err == nil {
			if announced {
				log("the observer answered again after %s", now().Sub(start).Round(time.Second))
			}
			return now().Sub(start), true
		}
		if ctx.Err() != nil {
			return now().Sub(start), false
		}
		// A zero budget means "check once and tell me", which is what a
		// caller who wants the old behaviour asks for explicitly.
		if budget <= 0 || !now().Before(deadline) {
			return now().Sub(start), false
		}
		if !announced {
			log("the observer is not answering; waiting up to %s before giving up rather than blaming the next entry", budget)
			announced = true
		}
		sleep(ctx, poll)
	}
}
