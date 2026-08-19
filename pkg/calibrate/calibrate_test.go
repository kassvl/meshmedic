package calibrate

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/prom"
)

// scriptedQuerier replays a fixed series of answers, one per Sample call.
// A NaN scripts an empty result; a negative sentinel scripts a query error.
type scriptedQuerier struct {
	values []float64
	i      int
}

const scriptError = -999999

func (q *scriptedQuerier) Query(context.Context, string) (float64, error) {
	if q.i >= len(q.values) {
		return 0, prom.ErrNoData
	}
	v := q.values[q.i]
	q.i++
	switch {
	case math.IsNaN(v):
		return 0, prom.ErrNoData
	case v == scriptError:
		return 0, errors.New("scrape failed")
	}
	return v, nil
}

func entry(threshold float64, comparison, forDur string) catalog.Scenario {
	return catalog.Scenario{
		ID:    "test-entry",
		Title: "test",
		Signal: catalog.Signal{
			PromQL:     "vector(1)",
			Comparison: comparison,
			Threshold:  threshold,
			For:        forDur,
		},
		Remediation: catalog.Remediation{
			Target:        catalog.Target{Kind: "VirtualService"},
			PatchTemplate: "kind: VirtualService",
		},
	}
}

// run replays the values at the given interval and returns the single
// observation the report contains.
func run(t *testing.T, s catalog.Scenario, cfg Config, interval time.Duration, values []float64) Observation {
	t.Helper()
	q := &scriptedQuerier{values: values}
	r := New(cfg, []catalog.Scenario{s}, []Target{{Params: map[string]string{"service": "payments"}}}, q)
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for i := range values {
		r.Sample(context.Background(), base.Add(time.Duration(i)*interval))
	}
	report := r.Report()
	if len(report) != 1 {
		t.Fatalf("report has %d observations, want 1", len(report))
	}
	return report[0]
}

func TestCalibratedWhenQuietWithRoomToSpare(t *testing.T) {
	// Healthy p99 around 50ms against a 1000ms threshold: 20x headroom.
	o := run(t, entry(1000, ">", "90s"), Config{}, 15*time.Second,
		[]float64{48, 51, 47, 55, 49, 52, 50, 46})

	if o.Verdict != Calibrated {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, Calibrated)
	}
	if o.Headroom < 15 {
		t.Errorf("headroom = %.3g, want roughly 1000/55 = 18", o.Headroom)
	}
	if o.LongestBreach != 0 {
		t.Errorf("longest breach = %v, want 0", o.LongestBreach)
	}
}

// The regression fixture this whole package was written for. On the live
// testbed, upstream-dependency-latency's signal hovered between 183 and 224ms
// against a 200ms threshold and fired with no fault injected. Nothing in the
// repository caught it: the metric resolved, so the metric checker said ok,
// and the lock said approved. Only a calibration measurement sees it.
func TestTheLedgerFlappingCaseIsCaughtAsMiscalibrated(t *testing.T) {
	s := entry(200, ">", "90s")
	// Values measured live on 2026-08-19, sampled every 20s. The signal holds
	// above 200 for well past the 90s hold duration.
	o := run(t, s, Config{}, 20*time.Second,
		[]float64{217.3, 224.3, 209.7, 211.4, 218.9, 205.2, 182.8})

	if o.Verdict != Miscalibrated {
		t.Fatalf("verdict = %q (%s), want %q: this exact signal fired on a healthy cluster",
			o.Verdict, o.Note, Miscalibrated)
	}
	if o.LongestBreach < 90*time.Second {
		t.Errorf("longest breach = %v, want at least the 90s hold duration", o.LongestBreach)
	}
	if o.Headroom >= 1 {
		t.Errorf("headroom = %.3g, want under 1: the healthy peak is above the threshold", o.Headroom)
	}
}

// A brief excursion that never reaches the hold duration must NOT be called
// miscalibrated. Calling it so would be a false positive, and this gate is
// worthless if it produces those.
func TestBriefExcursionIsMarginalNotMiscalibrated(t *testing.T) {
	// One sample over 200 at a 20s interval, against a 90s hold. The breach
	// cannot possibly have held long enough to fire.
	o := run(t, entry(200, ">", "90s"), Config{}, 20*time.Second,
		[]float64{120, 118, 205, 119, 121, 117, 122})

	if o.Verdict != Marginal {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, Marginal)
	}
	if o.LongestBreach >= 90*time.Second {
		t.Errorf("longest breach = %v, want under the hold duration", o.LongestBreach)
	}
}

// The failure mode silence-only testing misses entirely: quiet, never
// breaching, and one ordinary busy afternoon away from firing.
func TestQuietButTooCloseIsMarginal(t *testing.T) {
	// Peak 180 against a 200 threshold: 1.11x headroom, under the default 2x.
	o := run(t, entry(200, ">", "90s"), Config{}, 15*time.Second,
		[]float64{170, 175, 180, 172, 178, 169, 176})

	if o.Verdict != Marginal {
		t.Fatalf("verdict = %q (%s), want %q: it never fired but has almost no headroom",
			o.Verdict, o.Note, Marginal)
	}
	if o.LongestBreach != 0 {
		t.Errorf("longest breach = %v, want 0: it never crossed", o.LongestBreach)
	}
	if o.Headroom < 1 || o.Headroom > 1.3 {
		t.Errorf("headroom = %.3g, want about 1.11", o.Headroom)
	}
}

// The most dangerous confusion available here. An entry whose metric does not
// exist is perfectly silent. Grading that as calibrated would award the best
// possible score to a dead entry, which is how retry-storm-damping looked
// healthy to a name-level check.
func TestNoDataIsUnmeasuredNotCalibrated(t *testing.T) {
	nan := math.NaN()
	o := run(t, entry(1000, ">", "90s"), Config{}, 15*time.Second,
		[]float64{nan, nan, nan, nan, nan, nan, nan, nan})

	if o.Verdict != Unmeasured {
		t.Fatalf("verdict = %q (%s), want %q: an entry nobody could measure is not calibrated",
			o.Verdict, o.Note, Unmeasured)
	}
	if o.Empty != 8 {
		t.Errorf("empty = %d, want 8", o.Empty)
	}
	if !math.IsNaN(o.Headroom) {
		t.Errorf("headroom = %v, want NaN: there is nothing to compute it from", o.Headroom)
	}
}

// An empty result must never be folded into the numbers as a zero. A zero
// would look like the safest possible reading and would drag the extreme down.
func TestEmptyResultsDoNotCountAsZero(t *testing.T) {
	nan := math.NaN()
	// Five real samples near the threshold, interleaved with empties.
	o := run(t, entry(200, ">", "90s"), Config{MinSamples: 5}, 15*time.Second,
		[]float64{190, nan, 195, nan, 188, nan, 192, 191})

	if o.Samples != 5 {
		t.Fatalf("samples = %d, want 5 resolved readings", o.Samples)
	}
	if o.Empty != 3 {
		t.Errorf("empty = %d, want 3", o.Empty)
	}
	if o.Min != 188 {
		t.Errorf("min = %v, want 188: an empty result is not a zero", o.Min)
	}
	if o.Verdict != Marginal {
		t.Errorf("verdict = %q (%s), want %q", o.Verdict, o.Note, Marginal)
	}
}

// Too few resolved samples is not evidence of calibration either. One lucky
// reading must not certify an entry.
func TestTooFewSamplesIsUnmeasured(t *testing.T) {
	nan := math.NaN()
	o := run(t, entry(1000, ">", "90s"), Config{MinSamples: 5}, 15*time.Second,
		[]float64{50, nan, nan, nan, nan})

	if o.Verdict != Unmeasured {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, Unmeasured)
	}
}

// Query errors are not readings and must not be graded as ones.
func TestQueryErrorsAreNotReadings(t *testing.T) {
	o := run(t, entry(1000, ">", "90s"), Config{MinSamples: 5}, 15*time.Second,
		[]float64{scriptError, scriptError, scriptError, scriptError, scriptError, scriptError})

	if o.Verdict != Unmeasured {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, Unmeasured)
	}
	if o.Errors != 6 {
		t.Errorf("errors = %d, want 6", o.Errors)
	}
	if o.Samples != 0 {
		t.Errorf("samples = %d, want 0", o.Samples)
	}
}

// An entry with no hold duration fires on the first breaching sample, so any
// breach at all is miscalibration.
func TestZeroHoldDurationFiresOnAnySingleBreach(t *testing.T) {
	o := run(t, entry(200, ">", ""), Config{}, 15*time.Second,
		[]float64{100, 110, 205, 105, 108, 102, 109})

	if o.Verdict != Miscalibrated {
		t.Fatalf("verdict = %q (%s), want %q: with no hold duration a single breach fires",
			o.Verdict, o.Note, Miscalibrated)
	}
}

// A less-than entry is threatened by its minimum, not its maximum. Getting
// this backwards would report every absence-style entry as safe.
func TestLessThanComparisonUsesTheMinimum(t *testing.T) {
	// Threshold: fire when below 0.05. Healthy traffic sits around 8 rps.
	o := run(t, entry(0.05, "<", "60s"), Config{}, 15*time.Second,
		[]float64{8.2, 7.9, 8.5, 8.1, 7.7, 8.3, 8.0})

	if o.Verdict != Calibrated {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, Calibrated)
	}
	// headroom = min/threshold = 7.7/0.05 = 154
	if o.Headroom < 100 {
		t.Errorf("headroom = %.3g, want about 154 (min over threshold)", o.Headroom)
	}
}

func TestLessThanCatchesADipThatWouldFire(t *testing.T) {
	// Traffic collapses and stays collapsed for well past the hold duration.
	o := run(t, entry(0.05, "<", "60s"), Config{}, 20*time.Second,
		[]float64{8.2, 0.0, 0.0, 0.0, 0.0, 0.0})

	if o.Verdict != Miscalibrated {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, Miscalibrated)
	}
}

// A breach that stops and restarts is two intervals, not one long one.
// Summing them would invent a breach that never happened.
func TestInterruptedBreachesAreNotSummed(t *testing.T) {
	// Two separate 40s breaches at a 20s interval, never 90s continuous.
	o := run(t, entry(200, ">", "90s"), Config{}, 20*time.Second,
		[]float64{210, 215, 212, 100, 100, 220, 218, 216, 100, 100})

	if o.Verdict == Miscalibrated {
		t.Fatalf("verdict = %q (%s): two short breaches were summed into a long one",
			o.Verdict, o.Note)
	}
	if o.LongestBreach >= 90*time.Second {
		t.Errorf("longest breach = %v, want under 90s: the breaches were interrupted", o.LongestBreach)
	}
	if o.Verdict != Marginal {
		t.Errorf("verdict = %q, want %q", o.Verdict, Marginal)
	}
}

// A signal that is exactly zero on a healthy cluster (a failure-flag counter)
// has unbounded headroom, which is the correct reading rather than a divide
// by zero.
func TestZeroSignalHasInfiniteHeadroom(t *testing.T) {
	o := run(t, entry(0.5, ">", "60s"), Config{}, 15*time.Second,
		[]float64{0, 0, 0, 0, 0, 0, 0})

	if o.Verdict != Calibrated {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, Calibrated)
	}
	if !math.IsInf(o.Headroom, 1) {
		t.Errorf("headroom = %v, want +Inf", o.Headroom)
	}
}

// An entry whose signal needs a parameter the target lacks was never a
// question for that target, and must not be graded as anything.
func TestMissingParameterIsNotApplicable(t *testing.T) {
	s := entry(200, ">", "90s")
	s.Signal.PromQL = `sum(rate(istio_requests_total{ns="{{.ingress_workload}}"}[2m]))`
	o := run(t, s, Config{}, 15*time.Second, []float64{1, 1, 1, 1, 1, 1})

	if o.Verdict != NotApplicable {
		t.Fatalf("verdict = %q (%s), want %q", o.Verdict, o.Note, NotApplicable)
	}
}

// The gate's own pass condition. Marginal and unmeasured both fail, because
// the entire reason this exists is that "it did not fire today" is not the
// same as "it is safe".
func TestPassedRequiresMoreThanNothingFiring(t *testing.T) {
	cases := []struct {
		name string
		sum  Summary
		want bool
	}{
		{"all calibrated", Summary{Calibrated: 19}, true},
		{"one miscalibrated", Summary{Calibrated: 18, Miscalibrated: 1}, false},
		{"one marginal", Summary{Calibrated: 18, Marginal: 1}, false},
		{"one unmeasured", Summary{Calibrated: 18, Unmeasured: 1}, false},
		{"not-applicable does not fail", Summary{Calibrated: 17, NotApplicable: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sum.Passed(); got != tc.want {
				t.Errorf("Passed() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The report leads with what needs attention, so an operator reading the top
// of the output reads the worst news first.
func TestReportSortsWorstFirst(t *testing.T) {
	scenarios := []catalog.Scenario{}
	for _, spec := range []struct {
		id        string
		threshold float64
	}{
		{"a-calibrated", 10000},
		{"b-miscalibrated", 10},
		{"c-marginal", 300},
	} {
		s := entry(spec.threshold, ">", "30s")
		s.ID = spec.id
		scenarios = append(scenarios, s)
	}
	q := &scriptedQuerier{}
	r := New(Config{MinSamples: 3}, scenarios, []Target{{Params: map[string]string{}}}, q)
	// One querier for three scenarios: return a steady 200 to everything, so
	// a-calibrated is far under, b-miscalibrated is far over, c-marginal is
	// close under.
	r.querier = steady(200)
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		r.Sample(context.Background(), base.Add(time.Duration(i)*20*time.Second))
	}

	report := r.Report()
	if len(report) != 3 {
		t.Fatalf("report has %d rows, want 3", len(report))
	}
	if report[0].Verdict != Miscalibrated {
		t.Errorf("first row is %q (%s), want the miscalibrated entry first",
			report[0].Scenario, report[0].Verdict)
	}
	if report[len(report)-1].Verdict != Calibrated {
		t.Errorf("last row is %q, want the calibrated entry last", report[len(report)-1].Verdict)
	}
}

type steadyQuerier float64

func (s steadyQuerier) Query(context.Context, string) (float64, error) { return float64(s), nil }

func steady(v float64) Querier { return steadyQuerier(v) }

// Every entry in the shipped catalog must be sample-able without the runner
// panicking or mis-keying, whatever its shape.
func TestEveryShippedEntrySamplesCleanly(t *testing.T) {
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	targets := []Target{{Params: map[string]string{
		"service": "payments", "namespace": "demo", "workload": "payments-v2",
		"subset": "v2", "stable_subset": "v1",
	}}}
	r := New(Config{MinSamples: 3}, scenarios, targets, steady(0))
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		r.Sample(context.Background(), base.Add(time.Duration(i)*20*time.Second))
	}
	report := r.Report()
	if len(report) != len(scenarios) {
		t.Fatalf("report has %d rows for %d scenarios", len(report), len(scenarios))
	}
	// With every signal reading a steady zero, nothing may be reported as
	// miscalibrated: a zero is the healthiest possible reading for a
	// greater-than entry.
	for _, o := range report {
		if o.Verdict == Miscalibrated {
			t.Errorf("%s reported %q on an all-zero signal: %s", o.Scenario, o.Verdict, o.Note)
		}
	}
}
