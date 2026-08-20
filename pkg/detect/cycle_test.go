package detect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

// The cycle summary is the one line an operator reads on every tick, and for
// a while it said "firing" for three different situations: an incident that
// was reported, one that was suppressed as a cascade symptom, and one that
// had not yet held for its `for` duration. Two runs were misread because of
// it. These tests defend the distinction.

// assertAccounted is the invariant the whole change rests on: every breach
// lands in exactly one bucket. If it does not, some breach is either
// invisible in the summary or counted twice, and the number is a liability
// again rather than a fact.
func assertAccounted(t *testing.T, c Cycle) {
	t.Helper()
	sum := c.Reported + c.Suppressed + c.Waiting + c.RateLimited + c.Retrying
	if sum != c.InBreach {
		t.Errorf("in breach = %d but buckets sum to %d (reported %d, suppressed %d, waiting %d, rate-limited %d, retrying %d)",
			c.InBreach, sum, c.Reported, c.Suppressed, c.Waiting, c.RateLimited, c.Retrying)
	}
}

// A breach that has not held for its `for` duration reports nothing, which is
// correct. The bug was that it looked exactly like a healthy cluster in the
// summary while the word "firing" said otherwise.
func TestBreachAwaitingItsHoldIsNotReported(t *testing.T) {
	s := testScenario()
	s.Signal.For = "90s"

	d := New([]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(context.Context, Incident) error {
			t.Error("delivered an incident before its hold duration elapsed")
			return nil
		})

	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	c := d.Tick(context.Background(), base)

	assertAccounted(t, c)
	if c.InBreach != 1 {
		t.Fatalf("in breach = %d, want 1", c.InBreach)
	}
	if c.Waiting != 1 || c.Reported != 0 {
		t.Errorf("waiting = %d, reported = %d; want 1 and 0", c.Waiting, c.Reported)
	}
	if got := c.Line(); !strings.Contains(got, "1 in breach (1 awaiting hold)") {
		t.Errorf("summary line does not say the breach is waiting:\n  %s", got)
	}
}

// A suppressed cascade symptom is a real breach and deliberately not a second
// report. Counting it as firing made one fault look like two incidents.
func TestSuppressedCascadeIsCountedApartFromTheReport(t *testing.T) {
	cause := testScenario()
	cause.ID = "pool-overflow"
	cause.Signal.PromQL = "cause_signal"
	cause.Signal.For = ""
	cause.Suppresses = []string{"error-surge"}

	symptom := testScenario()
	symptom.ID = "error-surge"
	symptom.Signal.PromQL = "symptom_signal"
	symptom.Signal.For = ""

	causeVal, symptomVal := 1.0, 1.0
	var fired []Incident
	d := New([]catalog.Scenario{cause, symptom},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		valueByQuery{"cause_signal": &causeVal, "symptom_signal": &symptomVal},
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil })

	c := d.Tick(context.Background(), time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

	assertAccounted(t, c)
	if c.InBreach != 2 {
		t.Fatalf("in breach = %d, want 2: both signals are over their thresholds", c.InBreach)
	}
	if c.Reported != 1 || c.Suppressed != 1 {
		t.Errorf("reported = %d, suppressed = %d; want 1 and 1", c.Reported, c.Suppressed)
	}
	if len(fired) != 1 {
		t.Errorf("delivered %d incidents, want 1", len(fired))
	}
	if got := c.Line(); !strings.Contains(got, "2 in breach (1 reported, 1 suppressed)") {
		t.Errorf("summary line hides the suppression:\n  %s", got)
	}
}

// An episode delivered on an earlier tick is still reported on later ones. It
// must not drift into "awaiting hold", which would suggest nothing had been
// said about an incident that is open.
func TestAnOpenEpisodeKeepsCountingAsReported(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""

	var fired []Incident
	d := New([]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil })

	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	first := d.Tick(context.Background(), base)
	second := d.Tick(context.Background(), base.Add(30*time.Second))

	assertAccounted(t, first)
	assertAccounted(t, second)
	if first.Reported != 1 {
		t.Errorf("first tick reported = %d, want 1", first.Reported)
	}
	if second.Reported != 1 || second.Waiting != 0 {
		t.Errorf("second tick reported = %d, waiting = %d; want 1 and 0", second.Reported, second.Waiting)
	}
	if len(fired) != 1 {
		t.Errorf("delivered %d incidents across two ticks, want 1", len(fired))
	}
}

// A handler that errors keeps the episode for retry, so nothing was reported
// and the breach is not resolved either. Counting it as reported would claim
// a document that does not exist.
func TestHandlerFailureCountsAsRetrying(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""

	d := New([]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(context.Context, Incident) error { return errors.New("pull request refused") })

	c := d.Tick(context.Background(), time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

	assertAccounted(t, c)
	if c.Retrying != 1 || c.Reported != 0 {
		t.Errorf("retrying = %d, reported = %d; want 1 and 0", c.Retrying, c.Reported)
	}
	if got := c.Line(); !strings.Contains(got, "handler retrying") {
		t.Errorf("summary line does not mention the retry:\n  %s", got)
	}
}

// The guardrail stops a further proposal without silencing the signal, so the
// operator has to be able to tell "nothing was proposed because of the rate
// limit" from "nothing is wrong". A guardrail that hides what it blocked is
// its own fail-open.
func TestRateLimitedBreachIsCountedApartFromReported(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	s.Guardrails.MaxAppliesPerHour = 1

	value := 1.0
	var fired []Incident
	d := New([]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		valueByQuery{"vector(1)": &value},
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil })

	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	first := d.Tick(context.Background(), base)
	if first.Reported != 1 {
		t.Fatalf("first tick reported = %d, want 1", first.Reported)
	}

	// Clear the signal so the episode closes, then breach again inside the
	// same hour: the second incident is due, and the budget is spent.
	value = 0
	d.Tick(context.Background(), base.Add(1*time.Minute))
	value = 1
	c := d.Tick(context.Background(), base.Add(2*time.Minute))

	assertAccounted(t, c)
	if c.RateLimited != 1 || c.Reported != 0 {
		t.Errorf("rate-limited = %d, reported = %d; want 1 and 0", c.RateLimited, c.Reported)
	}
	if len(fired) != 1 {
		t.Errorf("delivered %d incidents, want 1: the second was over the hourly budget", len(fired))
	}
	if got := c.Line(); !strings.Contains(got, "rate-limited") {
		t.Errorf("summary line hides that the guardrail blocked a proposal:\n  %s", got)
	}
}

// A quiet cluster must stay quiet in the summary: no breakdown, no noise.
func TestQuietClusterRendersWithoutABreakdown(t *testing.T) {
	c := Cycle{Observed: 1, InBreach: 0, Clear: 15, Unlocked: 2}
	got := c.Line()
	if !strings.Contains(got, "0 in breach,") {
		t.Errorf("quiet line should carry a bare zero:\n  %s", got)
	}
	if strings.Contains(got, "(") && strings.Contains(got, "reported") {
		t.Errorf("quiet line should not carry a breakdown:\n  %s", got)
	}
}
