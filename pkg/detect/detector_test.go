package detect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/prom"
)

type querierFunc func(ctx context.Context, promql string) (float64, error)

func (f querierFunc) Query(ctx context.Context, promql string) (float64, error) {
	return f(ctx, promql)
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
		func(_ context.Context, inc Incident) { fired = append(fired, inc) },
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
		func(_ context.Context, inc Incident) { fired = append(fired, inc) },
	)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 1 {
		t.Fatalf("got %d incidents, want 1 on the first tick", len(fired))
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
		func(_ context.Context, inc Incident) { fired = append(fired, inc) },
	)
	d.Tick(context.Background(), time.Now())
	if len(fired) != 0 {
		t.Fatalf("got %d incidents, want 0: target filter must exclude the scenario", len(fired))
	}
}
