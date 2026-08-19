package detect

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/baseline"
	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
	"github.com/kassvl/meshmedic/pkg/recorder"
)

// testProbe is the coverage-probe query every fake below answers with one
// series, so a test exercising the incident state machine is not also
// exercising the probe. The tests that are about coverage script it directly.
const testProbe = "test-coverage-probe"

type querierFunc func(ctx context.Context, promql string) (float64, error)

func (f querierFunc) Query(ctx context.Context, promql string) (float64, error) {
	return f(ctx, promql)
}

// QuerySeries reuses the scripted scalar as a single unlabeled sample; the
// state-machine tests have no evidence queries, so this only satisfies the
// interface. Labeled evidence has its own fake below.
func (f querierFunc) QuerySeries(ctx context.Context, promql string) ([]prom.Sample, error) {
	if promql == testProbe {
		return []prom.Sample{{Value: 1}}, nil
	}
	v, err := f(ctx, promql)
	if err != nil {
		return nil, err
	}
	return []prom.Sample{{Value: v}}, nil
}

func testScenario() catalog.Scenario {
	return catalog.Scenario{
		ID:    "test-scenario",
		Title: "test",
		Signal: catalog.Signal{
			PromQL:     "vector(1)",
			Comparison: ">",
			Threshold:  0.5,
			For:        "60s",
		},
		Remediation: catalog.Remediation{
			Target:        catalog.Target{Kind: "VirtualService"},
			PatchTemplate: "kind: VirtualService",
		},
	}
}

// scripted runs the detector against a timeline of (offset, value) steps and
// returns the incidents it fired. A negative value scripts a query error,
// value -2 scripts ErrNoData.
func scripted(t *testing.T, steps []struct {
	offset time.Duration
	value  float64
}) []Incident {
	t.Helper()
	var fired []Incident
	idx := 0
	q := querierFunc(func(context.Context, string) (float64, error) {
		v := steps[idx].value
		switch {
		case v == -2:
			return 0, prom.ErrNoData
		case v < 0:
			return 0, errors.New("scrape failed")
		}
		return v, nil
	})
	d := New(
		[]catalog.Scenario{testScenario()},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		q,
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for idx = 0; idx < len(steps); idx++ {
		d.Tick(context.Background(), base.Add(steps[idx].offset))
	}
	return fired
}

// A catalog entry whose signal needs a param the target does not define is
// not an error: the scenario simply does not apply to that target. The skip
// is logged once, not on every tick, so a whole-catalog watch stays readable.
func TestMissingParamSkipsScenarioForTarget(t *testing.T) {
	s := testScenario()
	s.Signal.PromQL = `sum(rate(requests{workload="{{.ingress_workload}}"}[2m]))`
	var logged []string
	var fired []Incident
	q := querierFunc(func(context.Context, string) (float64, error) { return 1, nil })
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{"service": "payments"}}},
		q,
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Log = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		d.Tick(context.Background(), base.Add(time.Duration(i)*30*time.Second))
	}
	if len(fired) != 0 {
		t.Fatalf("scenario fired despite a missing param: %+v", fired)
	}
	skips := 0
	for _, l := range logged {
		if strings.Contains(l, "not applicable to this target") {
			skips++
		}
	}
	if skips != 1 {
		t.Fatalf("got %d skip log lines over 3 ticks, want exactly 1: %v", skips, logged)
	}
}

func TestFiresOnlyAfterForDurationHolds(t *testing.T) {
	fired := scripted(t, []struct {
		offset time.Duration
		value  float64
	}{
		{0, 1},                 // breach starts, pending
		{30 * time.Second, 1},  // still pending
		{60 * time.Second, 1},  // held 60s, fires
		{90 * time.Second, 1},  // stays firing, no duplicate
		{120 * time.Second, 0}, // clears
		{150 * time.Second, 1}, // new episode, pending
		{210 * time.Second, 1}, // held 60s again, fires again
	})
	if len(fired) != 2 {
		t.Fatalf("got %d incidents, want 2", len(fired))
	}
	if !fired[0].Since.Equal(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("first incident since %v, want breach start", fired[0].Since)
	}
}

func TestBlipResetsTheClock(t *testing.T) {
	fired := scripted(t, []struct {
		offset time.Duration
		value  float64
	}{
		{0, 1},                 // pending
		{30 * time.Second, 0},  // blip clears it
		{60 * time.Second, 1},  // pending again from here
		{90 * time.Second, 1},  // only 30s held, must not fire
		{120 * time.Second, 1}, // 60s held, fires
	})
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1", len(fired))
	}
	want := time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC)
	if !fired[0].Since.Equal(want) {
		t.Fatalf("incident since %v, want %v (after the blip)", fired[0].Since, want)
	}
}

func TestQueryErrorPreservesPendingState(t *testing.T) {
	fired := scripted(t, []struct {
		offset time.Duration
		value  float64
	}{
		{0, 1},                 // pending
		{30 * time.Second, -1}, // scrape error, state survives
		{60 * time.Second, 1},  // held 60s from start, fires
	})
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1: scrape errors must not reset a breach", len(fired))
	}
}

func TestNoDataClearsPendingState(t *testing.T) {
	fired := scripted(t, []struct {
		offset time.Duration
		value  float64
	}{
		{0, 1},                 // pending
		{30 * time.Second, -2}, // no data: traffic stopped, clear
		{60 * time.Second, 1},  // pending restarts here
		{90 * time.Second, 1},  // 30s held, must not fire
	})
	if len(fired) != 0 {
		t.Fatalf("got %d incidents, want 0: no-data must reset the breach", len(fired))
	}
}

func TestZeroForDurationFiresImmediately(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	var fired []Incident
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1 on the first tick", len(fired))
	}
}

func TestFailingHandlerKeepsTheEpisodeAlive(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	deliveries, failures := 0, 2
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, _ Incident) error {
			deliveries++
			if failures > 0 {
				failures--
				return errors.New("github brownout")
			}
			return nil
		},
	)
	now := time.Now()
	for i := 0; i < 5; i++ {
		d.Tick(context.Background(), now.Add(time.Duration(i)*time.Second))
	}
	if deliveries != 3 {
		t.Fatalf("got %d deliveries, want 3: retried until the handler succeeds, then quiet", deliveries)
	}
}

// evidenceQuerier serves the signal as a scalar and the evidence query as a
// labeled breakdown, mimicking a sum by (destination_workload) result.
type evidenceQuerier struct{}

func (evidenceQuerier) Query(context.Context, string) (float64, error) { return 1, nil }

func (evidenceQuerier) QuerySeries(_ context.Context, promql string) ([]prom.Sample, error) {
	if promql == testProbe {
		return []prom.Sample{{Value: 1}}, nil
	}
	return []prom.Sample{
		{Labels: map[string]string{"destination_workload": "payments-v2"}, Value: 0.19},
		{Labels: map[string]string{"destination_workload": "payments-v1"}, Value: 0.002},
	}, nil
}

func TestEvidenceKeepsLabels(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	s.Evidence = []catalog.Query{{Name: "errors-by-workload", PromQL: "vector(1)"}}
	var fired []Incident
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		evidenceQuerier{},
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1", len(fired))
	}
	ev := fired[0].Evidence
	if len(ev) != 1 || len(ev[0].Samples) != 2 {
		t.Fatalf("evidence %+v, want one result with two samples", ev)
	}
	if ev[0].Samples[0].Labels["destination_workload"] != "payments-v2" {
		t.Fatalf("first sample labels %v, want the workload name to survive", ev[0].Samples[0].Labels)
	}
}

// fakeObjects records the requested ref and serves a fixed deployment.
type fakeObjects struct{ gotRef string }

func (f *fakeObjects) Get(_ context.Context, apiVersion, kind, namespace, name string) (map[string]any, error) {
	f.gotRef = kind + " " + namespace + "/" + name
	return map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "app", "env": []any{
							map[string]any{"name": "TIMING_50_PERCENTILE", "value": "1200ms"},
						}},
					},
				},
			},
		},
	}, nil
}

func TestObjectEvidenceRendersTemplatesAndFields(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	s.ObjectEvidence = []catalog.ObjectQuery{{
		Name:       "canary-deployment-env",
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Object:     "{{.service}}-{{.subset}}",
		Namespace:  "{{.namespace}}",
		Fields:     []string{"spec.template.spec.containers[*].env"},
	}}
	objects := &fakeObjects{}
	var fired []Incident
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{"service": "payments", "subset": "v2", "namespace": "demo"}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Objects = objects
	d.Tick(context.Background(), time.Now())
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1", len(fired))
	}
	if objects.gotRef != "Deployment demo/payments-v2" {
		t.Fatalf("reader asked for %q, want the rendered Deployment demo/payments-v2", objects.gotRef)
	}
	oe := fired[0].ObjectEvidence
	if len(oe) != 1 || oe[0].Err != nil {
		t.Fatalf("object evidence %+v, want one clean result", oe)
	}
	got := oe[0].Fields["spec.template.spec.containers[*].env"]
	if got != "TIMING_50_PERCENTILE=1200ms" {
		t.Fatalf("field rendered %q, want TIMING_50_PERCENTILE=1200ms", got)
	}
}

func TestNilObjectReaderSkipsObjectEvidence(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	s.ObjectEvidence = []catalog.ObjectQuery{{
		Name: "x", APIVersion: "apps/v1", Kind: "Deployment", Object: "y", Fields: []string{"spec"},
	}}
	var fired []Incident
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 1 || fired[0].ObjectEvidence != nil {
		t.Fatalf("want the incident to fire with no object evidence when no reader is wired")
	}
}

// valueByQuery scripts each scenario's signal separately, keyed on a
// substring of the rendered PromQL.
type valueByQuery map[string]*float64

func (v valueByQuery) Query(_ context.Context, promql string) (float64, error) {
	for sub, val := range v {
		if strings.Contains(promql, sub) {
			return *val, nil
		}
	}
	return 0, prom.ErrNoData
}

func (v valueByQuery) QuerySeries(ctx context.Context, promql string) ([]prom.Sample, error) {
	if promql == testProbe {
		return []prom.Sample{{Value: 1}}, nil
	}
	val, err := v.Query(ctx, promql)
	if err != nil {
		return nil, err
	}
	return []prom.Sample{{Value: val}}, nil
}

func TestSuppressionHoldsBackTheCascadeScenario(t *testing.T) {
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
	q := valueByQuery{"cause_signal": &causeVal, "symptom_signal": &symptomVal}

	var fired []Incident
	d := New(
		[]catalog.Scenario{cause, symptom},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		q,
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)

	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	d.Tick(context.Background(), base)
	if len(fired) != 1 || fired[0].Scenario.ID != "pool-overflow" {
		t.Fatalf("fired %+v, want only pool-overflow: the 5xx surge is its symptom", ids(fired))
	}

	// Cause clears, symptom persists: the suppressed scenario must now fire.
	causeVal = 0
	d.Tick(context.Background(), base.Add(30*time.Second))
	if len(fired) != 2 || fired[1].Scenario.ID != "error-surge" {
		t.Fatalf("fired %v, want error-surge once its suppressor cleared", ids(fired))
	}
}

func ids(incidents []Incident) []string {
	out := make([]string, 0, len(incidents))
	for _, inc := range incidents {
		out = append(out, inc.Scenario.ID)
	}
	return out
}

// fakeTriage serves one namespace with a loadgen deployment whose logs
// carry a resolver failure and whose latest rollout changed the target.
type fakeTriage struct{}

func (fakeTriage) DeploymentNames(context.Context, string) ([]string, error) {
	return []string{"loadgen", "payments-v1"}, nil
}

func (fakeTriage) Logs(_ context.Context, _ string, deployment string, _, _ int) (string, error) {
	if deployment == "loadgen" {
		return "curl: (6) Could not resolve host: payments-svc.demo\nok line\n", nil
	}
	return "healthy\n", nil
}

func (fakeTriage) RecentRollouts(context.Context, string, time.Duration) ([]kube.Rollout, error) {
	return []kube.Rollout{{Deployment: "loadgen", AgeSeconds: 120, Diff: "- old\n+ new"}}, nil
}

func TestTriageEvidenceGathering(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	s.LogEvidence = []catalog.LogQuery{{
		Name: "client-failure-log-sweep", Namespace: "{{.namespace}}",
		Patterns: []string{"could not resolve"}, SinceSeconds: 300, MaxLines: 5,
	}}
	s.RolloutEvidence = []catalog.RolloutQuery{{
		Name: "recent-rollouts", Namespace: "{{.namespace}}", WithinMinutes: 30,
	}}
	var fired []Incident
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{"namespace": "demo"}}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Triage = fakeTriage{}
	d.Tick(context.Background(), time.Now())
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1", len(fired))
	}
	le := fired[0].LogEvidence
	if len(le) != 1 || le[0].Err != nil {
		t.Fatalf("log evidence %+v, want one clean result", le)
	}
	if got := le[0].Matches["loadgen"]; len(got) != 1 || !strings.Contains(got[0], "Could not resolve") {
		t.Fatalf("loadgen matches %v, want the resolver line (case-insensitive match)", got)
	}
	if len(le[0].Matches["payments-v1"]) != 0 {
		t.Fatalf("payments-v1 should have no matches, got %v", le[0].Matches["payments-v1"])
	}
	re := fired[0].RolloutEvidence
	if len(re) != 1 || re[0].Err != nil || len(re[0].Rollouts) != 1 || re[0].Rollouts[0].Deployment != "loadgen" {
		t.Fatalf("rollout evidence %+v, want loadgen's rollout", re)
	}
}

func TestBaselineRelativeThresholdFiresOnDeviation(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	s.Signal.Comparison = ">"
	s.Signal.Threshold = 1000 // static threshold is deliberately high
	s.Signal.BaselineMultiplier = 3
	s.Signal.BaselineMinSamples = 5

	value := 100.0
	q := querierFunc(func(context.Context, string) (float64, error) { return value, nil })
	var fired []Incident
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{"service": "payments"}}},
		q,
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Baseline = baseline.New(filepath.Join(t.TempDir(), "b.json"), 0.3)

	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	// Warm up: six healthy ticks at 100. Baseline learns ~100; nothing fires
	// (100 is under both the static 1000 and the relative 3x100=300).
	for i := 0; i < 6; i++ {
		d.Tick(context.Background(), base.Add(time.Duration(i)*time.Second))
	}
	if len(fired) != 0 {
		t.Fatalf("fired %d during warm-up, want 0", len(fired))
	}

	// A value under the static threshold (1000) but over the relative one
	// (3 x ~100 = ~300) must fire, proving the baseline drove the decision.
	value = 350
	d.Tick(context.Background(), base.Add(10*time.Second))
	if len(fired) != 1 {
		t.Fatalf("fired %d after deviation to 350, want 1: relative threshold should catch 3.5x normal", len(fired))
	}
}

func TestBaselineRelativeDoesNotFireBeforeWarmup(t *testing.T) {
	s := testScenario()
	s.Signal.For = ""
	s.Signal.Comparison = ">"
	s.Signal.Threshold = 1000
	s.Signal.BaselineMultiplier = 3
	s.Signal.BaselineMinSamples = 5

	// A high value on the very first tick (no baseline yet) must fall back to
	// the static threshold of 1000, so 350 does not fire during warm-up.
	q := querierFunc(func(context.Context, string) (float64, error) { return 350, nil })
	var fired []Incident
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		q,
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Baseline = baseline.New(filepath.Join(t.TempDir(), "b.json"), 0.3)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 0 {
		t.Fatal("fired before baseline warm-up; must fall back to the static threshold")
	}
}

type fakeRecorder struct{ fps []recorder.Fingerprint }

func (f *fakeRecorder) Record(fp recorder.Fingerprint) error {
	f.fps = append(f.fps, fp)
	return nil
}

func anomalyDetector(t *testing.T, scenarioThreshold float64, valuePtr *float64, rec *fakeRecorder) *Detector {
	t.Helper()
	s := testScenario()
	s.Signal.For = ""
	s.Signal.Comparison = ">"
	s.Signal.Threshold = scenarioThreshold
	d := New(
		[]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{"service": "payments"}}},
		querierFunc(func(context.Context, string) (float64, error) { return *valuePtr, nil }),
		func(context.Context, Incident) error { return nil },
	)
	d.Baseline = baseline.New(filepath.Join(t.TempDir(), "b.json"), 0.3)
	d.Recorder = rec
	d.AnomalyWatch = []catalog.Query{{Name: "5xx-rate", PromQL: "vector(1)"}}
	d.AnomalyMinSamples = 5
	d.AnomalyFactor = 3
	return d
}

func TestAnomalyRecorderRecordsUnexplainedDeviation(t *testing.T) {
	value := 10.0
	rec := &fakeRecorder{}
	// Scenario threshold is high, so the catalog never fires and the anomaly
	// deviation is genuinely unexplained.
	d := anomalyDetector(t, 1000, &value, rec)

	base := time.Now()
	for i := 0; i < 6; i++ {
		d.Tick(context.Background(), base.Add(time.Duration(i)*time.Second))
	}
	if len(rec.fps) != 0 {
		t.Fatalf("recorded during warm-up: %v", rec.fps)
	}
	value = 40 // 4x the ~10 baseline, past the factor of 3
	d.Tick(context.Background(), base.Add(10*time.Second))
	if len(rec.fps) != 1 {
		t.Fatalf("got %d fingerprints, want 1 after a 4x deviation", len(rec.fps))
	}
	if rec.fps[0].Signal != "5xx-rate" || rec.fps[0].Factor < 3 {
		t.Fatalf("fingerprint %+v, want the 5xx-rate signal at >3x", rec.fps[0])
	}
}

func TestAnomalyRecorderStaysQuietWhenCatalogExplainsIt(t *testing.T) {
	value := 10.0
	rec := &fakeRecorder{}
	// Scenario threshold is 20: the deviation to 40 also puts the catalog
	// scenario in breach, so the anomaly is explained and must not record.
	d := anomalyDetector(t, 20, &value, rec)

	base := time.Now()
	for i := 0; i < 6; i++ {
		d.Tick(context.Background(), base.Add(time.Duration(i)*time.Second))
	}
	value = 40 // deviates for the anomaly, and breaches the catalog scenario
	d.Tick(context.Background(), base.Add(10*time.Second))
	if len(rec.fps) != 0 {
		t.Fatalf("recorded %v while the catalog scenario was in breach; a catalog-explained anomaly must not be recorded", rec.fps)
	}
}

func TestTargetScenarioFilter(t *testing.T) {
	var fired []Incident
	d := New(
		[]catalog.Scenario{testScenario()},
		[]Target{{
			CoverageProbe: testProbe,
			Params:        map[string]string{},
			Scenarios:     []string{"some-other-scenario"},
		}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 0 {
		t.Fatalf("got %d incidents, want 0: target filter must exclude the scenario", len(fired))
	}
}

// scriptedWithResolve runs the timeline like scripted but also captures
// resolutions, so the closed-loop tests can assert on both edges.
func scriptedWithResolve(t *testing.T, steps []struct {
	offset time.Duration
	value  float64
}) ([]Incident, []Resolution) {
	t.Helper()
	var fired []Incident
	var resolved []Resolution
	idx := 0
	q := querierFunc(func(context.Context, string) (float64, error) {
		v := steps[idx].value
		switch {
		case v == -2:
			return 0, prom.ErrNoData
		case v < 0:
			return 0, errors.New("scrape failed")
		}
		return v, nil
	})
	d := New(
		[]catalog.Scenario{testScenario()},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		q,
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.OnResolve = func(_ context.Context, r Resolution) error { resolved = append(resolved, r); return nil }
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for idx = 0; idx < len(steps); idx++ {
		d.Tick(context.Background(), base.Add(steps[idx].offset))
	}
	return fired, resolved
}

func TestResolutionReportsOnRecovery(t *testing.T) {
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	fired, resolved := scriptedWithResolve(t, []struct {
		offset time.Duration
		value  float64
	}{
		{0, 1},                 // breach starts, pending
		{60 * time.Second, 1},  // held 60s, fires
		{90 * time.Second, 1},  // still firing
		{120 * time.Second, 0}, // recovers -> one resolution
		{150 * time.Second, 0}, // stays clear, no duplicate
	})
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1", len(fired))
	}
	if len(resolved) != 1 {
		t.Fatalf("got %d resolutions, want 1", len(resolved))
	}
	r := resolved[0]
	if !r.Since.Equal(base) {
		t.Errorf("resolution since %v, want breach start %v", r.Since, base)
	}
	if want := base.Add(120 * time.Second); !r.ResolvedAt.Equal(want) {
		t.Errorf("resolved at %v, want %v", r.ResolvedAt, want)
	}
	if want := 120 * time.Second; r.Duration() != want {
		t.Errorf("duration %v, want %v", r.Duration(), want)
	}
}

func TestNoResolutionWhenIncidentNeverFired(t *testing.T) {
	// A breach that clears before the for-duration never became an incident,
	// so there is nothing to resolve.
	_, resolved := scriptedWithResolve(t, []struct {
		offset time.Duration
		value  float64
	}{
		{0, 1},                // pending
		{30 * time.Second, 0}, // clears before the 60s for-duration
	})
	if len(resolved) != 0 {
		t.Fatalf("got %d resolutions, want 0 (never fired)", len(resolved))
	}
}

func TestNoResolutionWhenTrafficVanishes(t *testing.T) {
	// A firing incident going to no-data resets without a resolution: no
	// traffic is not the same as recovery.
	fired, resolved := scriptedWithResolve(t, []struct {
		offset time.Duration
		value  float64
	}{
		{0, 1},                 // pending
		{60 * time.Second, 1},  // fires
		{90 * time.Second, -2}, // ErrNoData clears state, but not a recovery
	})
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1", len(fired))
	}
	if len(resolved) != 0 {
		t.Fatalf("got %d resolutions, want 0 (no-data is not recovery)", len(resolved))
	}
}

// The invariant WS-1a exists for: an empty result, a zero value, and a query
// error are three different facts about the world, and a detector that folds
// them into one silent answer cannot be trusted with any of them.
func TestEmptyZeroAndErrorAreThreeDistinctOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		answer  func() (float64, error)
		outcome Outcome
		reason  string
	}{
		{"empty vector", func() (float64, error) { return 0, prom.ErrNoData }, OutcomeBlind, "no series"},
		{"zero value", func() (float64, error) { return 0, nil }, OutcomeClear, ""},
		{"query error", func() (float64, error) { return 0, errors.New("HTTP 500") }, OutcomeBlind, "query failed"},
		{"breaching value", func() (float64, error) { return 1, nil }, OutcomeFiring, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := querierFunc(func(context.Context, string) (float64, error) { return tc.answer() })
			d := New([]catalog.Scenario{testScenario()},
				[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
				q, func(context.Context, Incident) error { return nil })
			cycle := d.Tick(context.Background(), time.Now())

			if len(cycle.Evaluations) != 1 {
				t.Fatalf("evaluations = %d, want 1", len(cycle.Evaluations))
			}
			got := cycle.Evaluations[0]
			if got.Outcome != tc.outcome {
				t.Errorf("outcome = %q, want %q (reason %q)", got.Outcome, tc.outcome, got.Reason)
			}
			if tc.reason != "" && !strings.Contains(got.Reason, tc.reason) {
				t.Errorf("reason = %q, want it to mention %q", got.Reason, tc.reason)
			}
		})
	}
}

// A target whose coverage probe returns nothing is unobserved, and every
// scenario against it reports blind. Reporting clear here would be the
// fail-open bug: the tool would be asserting health it never measured.
func TestUnobservedTargetReportsBlindNotClear(t *testing.T) {
	// The signal itself would read as healthy; only the probe is dark.
	q := valueByQueryFunc(func(promql string) (float64, error) {
		if promql == "probe-that-returns-nothing" {
			return 0, prom.ErrNoData
		}
		return 0, nil
	})
	d := New([]catalog.Scenario{testScenario()},
		[]Target{{CoverageProbe: "probe-that-returns-nothing", Params: map[string]string{}}},
		q, func(context.Context, Incident) error { return nil })

	cycle := d.Tick(context.Background(), time.Now())

	if cycle.Observed != 0 || cycle.Unobserved != 1 {
		t.Fatalf("observed=%d unobserved=%d, want 0 and 1", cycle.Observed, cycle.Unobserved)
	}
	if cycle.Healthy() {
		t.Error("cycle reports healthy with an unobserved target; check --once would exit 0 while blind")
	}
	if cycle.Clear != 0 {
		t.Errorf("clear = %d, want 0: a blind detector must not report health", cycle.Clear)
	}
	if cycle.Blind != 1 {
		t.Fatalf("blind = %d, want 1", cycle.Blind)
	}
	if !strings.Contains(cycle.Evaluations[0].Reason, "unobserved") {
		t.Errorf("reason = %q, want it to name the coverage failure", cycle.Evaluations[0].Reason)
	}
}

// The critical subtlety: traffic-vanished-triage and the client-deploy family
// behind it detect an incident whose entire symptom is that telemetry stopped.
// Blind-detection must not suppress them. The coverage probe, not the scenario
// query, is what separates "traffic stopped" from "we cannot see this target".
func TestAbsenceIsSignalStillFiresWhenTheTargetIsVisible(t *testing.T) {
	s := testScenario()
	s.Signal.AbsenceIsSignal = true
	s.Signal.Comparison = "<"
	s.Signal.Threshold = 0.5 // an empty result is a zero, which is under it
	s.Signal.For = ""

	var fired []Incident
	q := querierFunc(func(context.Context, string) (float64, error) { return 0, prom.ErrNoData })
	d := New([]catalog.Scenario{s},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		q, func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil })

	cycle := d.Tick(context.Background(), time.Now())

	if len(fired) != 1 {
		t.Fatalf("fired %d incidents, want 1: absence is this entry's signal", len(fired))
	}
	if cycle.Blind != 0 {
		t.Errorf("blind = %d, want 0: an absence entry reading an empty result is measuring, not blind", cycle.Blind)
	}
}

// Same entry, but the target is not visible at all. Now the empty result
// proves nothing, and firing on it would be inventing an outage out of our
// own blindness.
func TestAbsenceIsSignalStaysQuietWhenTheTargetIsUnobserved(t *testing.T) {
	s := testScenario()
	s.Signal.AbsenceIsSignal = true
	s.Signal.Comparison = "<"
	s.Signal.Threshold = 0.5
	s.Signal.For = ""

	var fired []Incident
	// Every query, probe included, comes back empty: the mesh is dark to us.
	q := querierFunc(func(context.Context, string) (float64, error) { return 0, prom.ErrNoData })
	d := New([]catalog.Scenario{s},
		[]Target{{Params: map[string]string{"namespace": "demo"}}},
		q, func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil })

	cycle := d.Tick(context.Background(), time.Now())

	if cycle.Unobserved != 1 {
		t.Fatalf("unobserved = %d, want 1", cycle.Unobserved)
	}
	if len(fired) != 0 {
		t.Errorf("fired %d incidents from a target it cannot see", len(fired))
	}
}

// A breach measured while the target was visible must not mature into an
// incident across a stretch of blindness: the hold duration is a claim about
// what was observed, not about wall-clock time.
func TestBlindnessClearsPendingProgress(t *testing.T) {
	probeUp := true
	q := valueByQueryFunc(func(promql string) (float64, error) {
		if promql == testProbe {
			if !probeUp {
				return 0, prom.ErrNoData
			}
			return 1, nil
		}
		return 1, nil // always breaching
	})
	var fired []Incident
	d := New([]catalog.Scenario{testScenario()},
		[]Target{{CoverageProbe: testProbe, Params: map[string]string{}}},
		q, func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil })

	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	d.Tick(context.Background(), base) // pending starts

	probeUp = false
	d.Tick(context.Background(), base.Add(30*time.Second)) // blind: progress cleared

	probeUp = true
	d.Tick(context.Background(), base.Add(70*time.Second)) // 70s > 60s hold, but the clock restarted
	if len(fired) != 0 {
		t.Fatalf("fired %d incidents, want 0: the hold duration ran through a blind stretch", len(fired))
	}

	d.Tick(context.Background(), base.Add(140*time.Second)) // 70s of continuous sight
	if len(fired) != 1 {
		t.Errorf("fired %d incidents, want 1 once the hold held while observable", len(fired))
	}
}

// The audit's B-1: a restart re-opened every still-breaching incident, which
// in GitOps mode is a second pull request for an incident already under
// review. State persists, so the restarted process knows the incident is open.
func TestPersistedStateSurvivesRestartWithoutRefiring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	target := Target{CoverageProbe: testProbe, Params: map[string]string{"service": "payments"}}
	newDetector := func(fired *[]Incident) *Detector {
		q := querierFunc(func(context.Context, string) (float64, error) { return 1, nil })
		d := New([]catalog.Scenario{testScenario()}, []Target{target}, q,
			func(_ context.Context, inc Incident) error { *fired = append(*fired, inc); return nil })
		d.StateFile = path
		return d
	}
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	var firstRun []Incident
	d1 := newDetector(&firstRun)
	d1.Tick(context.Background(), base)
	d1.Tick(context.Background(), base.Add(90*time.Second)) // holds past 60s, fires
	if len(firstRun) != 1 {
		t.Fatalf("first run fired %d incidents, want 1", len(firstRun))
	}
	if err := d1.SaveState(path); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// A fresh process: same breach, still unresolved.
	var secondRun []Incident
	d2 := newDetector(&secondRun)
	if err := d2.LoadState(path); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	d2.Tick(context.Background(), base.Add(150*time.Second))
	d2.Tick(context.Background(), base.Add(300*time.Second))
	if len(secondRun) != 0 {
		t.Errorf("restart re-opened %d incidents that were already open", len(secondRun))
	}
}

// The state key must survive someone editing the config, not just restarting.
// An index-based key silently reassigns every open incident to a different
// target the moment a target is inserted above it.
func TestPersistedStateSurvivesTargetReordering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	payments := Target{CoverageProbe: testProbe, Params: map[string]string{"service": "payments"}}
	ledger := Target{CoverageProbe: testProbe, Params: map[string]string{"service": "ledger"}}
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	run := func(targets []Target, load bool) []Incident {
		var fired []Incident
		q := querierFunc(func(context.Context, string) (float64, error) { return 1, nil })
		d := New([]catalog.Scenario{testScenario()}, targets, q,
			func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil })
		d.StateFile = path
		if load {
			if err := d.LoadState(path); err != nil {
				t.Fatalf("LoadState: %v", err)
			}
		}
		d.Tick(context.Background(), base)
		d.Tick(context.Background(), base.Add(90*time.Second))
		if err := d.SaveState(path); err != nil {
			t.Fatalf("SaveState: %v", err)
		}
		return fired
	}

	if got := run([]Target{payments}, false); len(got) != 1 {
		t.Fatalf("first run fired %d, want 1", len(got))
	}
	// ledger is inserted ahead of payments. payments' incident is still open.
	got := run([]Target{ledger, payments}, true)
	for _, inc := range got {
		if inc.Params["service"] == "payments" {
			t.Error("payments re-fired after a target was inserted above it in the config")
		}
	}
	if len(got) != 1 || got[0].Params["service"] != "ledger" {
		t.Errorf("want exactly the new ledger incident, got %d incidents", len(got))
	}
}

// valueByQueryFunc is a fake whose answer depends on the query and on state
// the test mutates between ticks.
type valueByQueryFunc func(promql string) (float64, error)

func (f valueByQueryFunc) Query(_ context.Context, promql string) (float64, error) {
	return f(promql)
}

func (f valueByQueryFunc) QuerySeries(_ context.Context, promql string) ([]prom.Sample, error) {
	v, err := f(promql)
	if err != nil {
		return nil, err
	}
	return []prom.Sample{{Value: v}}, nil
}
