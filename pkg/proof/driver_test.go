package proof

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The distinction these tests defend is the whole point of Retryable: a run
// nobody could see is worth repeating, and a run that saw the entry fail is
// worth reading. Getting it backwards turns the harness into a machine for
// retrying until green.
func TestRetryableOnlyCoversBlindRuns(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		want bool
	}{
		{
			name: "blind: the observer died, so the run says nothing",
			res:  Result{Entry: "e", Blind: true, BlindReason: "port-forward died", Passed: false},
			want: true,
		},
		{
			name: "blind even though something fired before sight was lost",
			res:  Result{Entry: "e", Blind: true, Fired: true, Passed: false},
			want: true,
		},
		{
			name: "the entry did not fire on a healthy observer: a finding, not a flake",
			res:  Result{Entry: "e", Passed: false, Failures: []string{"never fired within 5m"}},
			want: false,
		},
		{
			name: "the entry fired but named the wrong workload: a finding",
			res:  Result{Entry: "e", Fired: true, Passed: false, Missing: []string{"payments-v2"}},
			want: false,
		},
		{
			name: "a neighbour fired that was not allowed to: a finding",
			res:  Result{Entry: "e", Fired: true, Passed: false, Unexpected: []string{"error-surge"}},
			want: false,
		},
		{
			name: "a pass is not retried",
			res:  Result{Entry: "e", Passed: true, Fired: true, Resolved: true},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Retryable(tc.res); got != tc.want {
				t.Errorf("Retryable = %v, want %v", got, tc.want)
			}
		})
	}
}

// flakyObserver refuses a fixed number of times and then answers, which is
// what a port-forward being restarted looks like from the outside.
type flakyObserver struct {
	refusals int
	calls    int
}

func (f *flakyObserver) Query(context.Context, string) (float64, error) {
	f.calls++
	if f.calls <= f.refusals {
		return 0, errors.New("connection refused")
	}
	return 1, nil
}

func TestWaitForObserverRidesOutARestart(t *testing.T) {
	obs := &flakyObserver{refusals: 3}
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	sleep := func(_ context.Context, d time.Duration) { now = now.Add(d) }

	waited, ok := WaitForObserver(context.Background(), obs, time.Minute, 5*time.Second, clock, sleep, nil)
	if !ok {
		t.Fatal("gave up on an observer that came back well inside the budget")
	}
	if waited != 15*time.Second {
		t.Errorf("waited %s, want 15s (three refusals at a 5s poll)", waited)
	}
	if obs.calls != 4 {
		t.Errorf("queried %d times, want 4", obs.calls)
	}
}

// The budget is the thing that keeps waiting from becoming hanging. When it
// runs out, refusing really is the right answer.
func TestWaitForObserverGivesUpWhenTheBudgetRunsOut(t *testing.T) {
	obs := &flakyObserver{refusals: 1 << 30}
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	sleep := func(_ context.Context, d time.Duration) { now = now.Add(d) }

	waited, ok := WaitForObserver(context.Background(), obs, 20*time.Second, 5*time.Second, clock, sleep, nil)
	if ok {
		t.Fatal("claimed the observer answered when it never did")
	}
	if waited < 20*time.Second {
		t.Errorf("gave up after %s, before spending the 20s budget", waited)
	}
}

// A zero budget is how a caller asks for the old check-once behaviour, and it
// must not silently wait.
func TestWaitForObserverWithNoBudgetChecksOnce(t *testing.T) {
	obs := &flakyObserver{refusals: 1}
	slept := false
	waited, ok := WaitForObserver(context.Background(), obs, 0, 5*time.Second,
		func() time.Time { return time.Unix(0, 0) },
		func(context.Context, time.Duration) { slept = true }, nil)
	if ok {
		t.Error("reported success from a refusing observer")
	}
	if slept {
		t.Error("slept despite a zero budget")
	}
	if waited != 0 {
		t.Errorf("waited %s with no budget", waited)
	}
	if obs.calls != 1 {
		t.Errorf("queried %d times, want exactly 1", obs.calls)
	}
}

// Cancellation has to win over the budget, or a suite cannot be interrupted
// while it is waiting for something that is never coming back.
func TestWaitForObserverStopsOnCancel(t *testing.T) {
	obs := &flakyObserver{refusals: 1 << 30}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Unix(0, 0)
	_, ok := WaitForObserver(ctx, obs, time.Hour, 5*time.Second,
		func() time.Time { return now },
		func(_ context.Context, d time.Duration) { now = now.Add(d); cancel() }, nil)
	if ok {
		t.Error("reported success after cancellation")
	}
}
