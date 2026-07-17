package detect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
)

type querierFunc func(ctx context.Context, promql string) (float64, error)

func (f querierFunc) Query(ctx context.Context, promql string) (float64, error) {
	return f(ctx, promql)
}

// QuerySeries reuses the scripted scalar as a single unlabeled sample; the
// state-machine tests have no evidence queries, so this only satisfies the
// interface. Labeled evidence has its own fake below.
func (f querierFunc) QuerySeries(ctx context.Context, promql string) ([]prom.Sample, error) {
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
		[]Target{{Params: map[string]string{}}},
		q,
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for idx = 0; idx < len(steps); idx++ {
		d.Tick(context.Background(), base.Add(steps[idx].offset))
	}
	return fired
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
		{0, 1},                // pending
		{30 * time.Second, -1}, // scrape error, state survives
		{60 * time.Second, 1}, // held 60s from start, fires
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
		[]Target{{Params: map[string]string{}}},
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
		[]Target{{Params: map[string]string{}}},
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

func (evidenceQuerier) QuerySeries(context.Context, string) ([]prom.Sample, error) {
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
		[]Target{{Params: map[string]string{}}},
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
		[]Target{{Params: map[string]string{"service": "payments", "subset": "v2", "namespace": "demo"}}},
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
		[]Target{{Params: map[string]string{}}},
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
		[]Target{{Params: map[string]string{}}},
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
		[]Target{{Params: map[string]string{"namespace": "demo"}}},
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

func TestTargetScenarioFilter(t *testing.T) {
	var fired []Incident
	d := New(
		[]catalog.Scenario{testScenario()},
		[]Target{{
			Params:    map[string]string{},
			Scenarios: []string{"some-other-scenario"},
		}},
		querierFunc(func(context.Context, string) (float64, error) { return 1, nil }),
		func(_ context.Context, inc Incident) error { fired = append(fired, inc); return nil },
	)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 0 {
		t.Fatalf("got %d incidents, want 0: target filter must exclude the scenario", len(fired))
	}
}
