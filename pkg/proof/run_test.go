package proof

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/prom"
)

// fakeProm answers the coverage probe and lets the test drive each scenario's
// signal by id, so a proof can be exercised without a cluster.
type fakeProm struct{ values map[string]float64 }

func (f *fakeProm) Query(_ context.Context, promql string) (float64, error) {
	for id, v := range f.values {
		if strings.Contains(promql, id) {
			return v, nil
		}
	}
	return 0, prom.ErrNoData
}

func (f *fakeProm) QuerySeries(_ context.Context, promql string) ([]prom.Sample, error) {
	if strings.HasPrefix(promql, "count(") {
		return []prom.Sample{{Value: 1}}, nil // coverage probe passes
	}
	v, err := f.Query(context.Background(), promql)
	if err != nil {
		return nil, err
	}
	return []prom.Sample{{Labels: map[string]string{"destination_service_name": "ledger"}, Value: v}}, nil
}

// scenario builds an entry whose signal query embeds its own id, so fakeProm
// can drive each one independently.
func scenario(id string, threshold float64, suppresses ...string) catalog.Scenario {
	return catalog.Scenario{
		ID:          id,
		Title:       id,
		Description: "test entry " + id,
		Signal: catalog.Signal{
			PromQL:     `sum(rate(marker_` + id + `[2m]))`,
			Comparison: ">",
			Threshold:  threshold,
			For:        "",
		},
		Evidence: []catalog.Query{{Name: "by-service", PromQL: `sum by (destination_service_name) (marker_` + id + `)`}},
		Remediation: catalog.Remediation{
			Action: "report-only",
			Target: catalog.Target{Kind: "Deployment"},
		},
		Rollback:   "n/a",
		Suppresses: suppresses,
	}
}

// harness wires a runner whose clock and sleeps are fake, so a proof with a
// three-minute budget runs instantly and deterministically.
func harness(t *testing.T, scenarios []catalog.Scenario, p *fakeProm) (*Runner, *[]string) {
	t.Helper()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	var ran []string
	var q detect.Querier
	if p != nil {
		q = p
	}
	r := NewRunner(scenarios, q, nil)
	r.Now = func() time.Time { return now }
	r.Sleep = func(_ context.Context, d time.Duration) { now = now.Add(d) }
	r.Poll = 10 * time.Second
	r.Exec = func(_ context.Context, argv []string) error {
		ran = append(ran, strings.Join(argv, " "))
		return nil
	}
	return r, &ran
}

func baseSpec() Spec {
	return Spec{
		Entry:   "subject",
		Summary: "test",
		Target:  map[string]string{"service": "payments", "namespace": "demo"},
		Inject:  []Command{{Run: []string{"inject"}}},
		Reset:   []Command{{Run: []string{"reset"}}},
		Expect:  Expect{Names: []string{"ledger"}},
	}
}

func TestPassingProofFiresNamesAndResets(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5}}
	r, ran := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)

	res := r.Run(context.Background(), s)

	if !res.Passed {
		t.Fatalf("proof failed: %v", res.Failures)
	}
	if !res.Fired {
		t.Error("entry did not fire")
	}
	if len(res.Named) != 1 || res.Named[0] != "ledger" {
		t.Errorf("named = %v, want [ledger]", res.Named)
	}
	if !strings.Contains(strings.Join(*ran, "|"), "reset") {
		t.Errorf("reset did not run: %v", *ran)
	}
}

// The assertion that separates a real proof from a smoke test: an entry that
// fires without naming the culprit has detected something and explained
// nothing, and the proof must fail.
func TestFiringWithoutNamingTheCulpritFails(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)
	s.Expect.Names = []string{"a-service-that-is-never-mentioned"}

	res := r.Run(context.Background(), s)

	if res.Passed {
		t.Fatal("proof passed although the report never named what to act on")
	}
	if !res.Fired {
		t.Error("the entry should still be recorded as having fired")
	}
	if len(res.Missing) != 1 {
		t.Errorf("missing = %v, want the unnamed culprit", res.Missing)
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "did not explain") {
		t.Errorf("failure text does not say what went wrong: %v", res.Failures)
	}
}

func TestNotFiringFails(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 0}} // never breaches
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(2 * time.Minute)

	res := r.Run(context.Background(), s)

	if res.Passed || res.Fired {
		t.Fatalf("proof passed with an entry that never fired: %+v", res)
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "did not fire") {
		t.Errorf("failures = %v", res.Failures)
	}
}

// A neighbour that fires when it was required to stay quiet means the
// suppression the entry claims is not working, and the proof must say so in
// those words rather than just failing.
func TestNeighbourThatShouldBeSuppressedFailsTheProof(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5, "noisy": 5}}
	// subject does NOT suppress noisy, so both fire.
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1), scenario("noisy", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)
	s.Expect.Quiet = []string{"noisy"}

	res := r.Run(context.Background(), s)

	if res.Passed {
		t.Fatal("proof passed although an entry required to stay quiet fired")
	}
	if len(res.Unexpected) != 1 || res.Unexpected[0] != "noisy" {
		t.Errorf("unexpected = %v, want [noisy]", res.Unexpected)
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "suppression this entry claims is not working") {
		t.Errorf("failures = %v", res.Failures)
	}
}

// The same neighbour, with the suppression actually declared in the catalog,
// must now stay quiet and the proof must pass.
func TestDeclaredSuppressionKeepsTheNeighbourQuiet(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5, "noisy": 5}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1, "noisy"), scenario("noisy", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)
	s.Expect.Quiet = []string{"noisy"}

	res := r.Run(context.Background(), s)

	if !res.Passed {
		t.Fatalf("proof failed although the catalog declares the suppression: %v", res.Failures)
	}
	if len(res.Unexpected) != 0 {
		t.Errorf("unexpected = %v, want none", res.Unexpected)
	}
}

// An entry nobody accounted for firing alongside the subject is a finding
// too: either the fault is less isolated than the proof claims, or that entry
// is miscalibrated. Either way the run must not be called green.
func TestUnaccountedNeighbourFailsTheProof(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5, "stranger": 5}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1), scenario("stranger", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)
	// stranger is neither in Quiet nor in AllowFiring.

	res := r.Run(context.Background(), s)

	if res.Passed {
		t.Fatal("proof passed with an unaccounted entry firing alongside the subject")
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "no one said it would") {
		t.Errorf("failures = %v", res.Failures)
	}
}

func TestAllowFiringAcceptsAKnownSecondIncident(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5, "expected": 5}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1), scenario("expected", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)
	s.Expect.AllowFiring = []string{"expected"}

	res := r.Run(context.Background(), s)

	if !res.Passed {
		t.Fatalf("proof failed although the second incident was declared: %v", res.Failures)
	}
}

// A proof that leaves its fault behind poisons every run after it, so reset
// must run even when the proof fails outright.
func TestResetRunsEvenWhenTheProofFails(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 0}} // never fires
	r, ran := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(1 * time.Minute)

	res := r.Run(context.Background(), s)

	if res.Passed {
		t.Fatal("proof should have failed")
	}
	joined := strings.Join(*ran, "|")
	if !strings.Contains(joined, "reset") {
		t.Errorf("reset did not run after a failed proof: %v", *ran)
	}
}

// And reset must still run when the caller cancels mid-proof.
func TestResetRunsOnCancellation(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 0}}
	r, ran := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := baseSpec()
	s.FiresWithin = Duration(2 * time.Minute)
	r.Run(ctx, s)

	if !strings.Contains(strings.Join(*ran, "|"), "reset") {
		t.Errorf("reset did not run on a cancelled proof: %v", *ran)
	}
}

// A failed injection must not be reported as a failed entry: the entry was
// never given a fault to detect.
func TestFailedInjectionIsReportedAsSuch(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)
	r.Exec = func(_ context.Context, argv []string) error {
		if argv[0] == "inject" {
			return context.DeadlineExceeded
		}
		return nil
	}

	s := baseSpec()
	s.FiresWithin = Duration(2 * time.Minute)

	res := r.Run(context.Background(), s)

	if res.Passed {
		t.Fatal("proof passed despite a failed injection")
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "inject failed") {
		t.Errorf("failures = %v, want the injection blamed rather than the entry", res.Failures)
	}
	if res.Fired {
		t.Error("entry should not be recorded as fired when no fault was injected")
	}
}

func TestSpecValidationRejectsUnprovableSpecs(t *testing.T) {
	cases := map[string]func(*Spec){
		"no entry":       func(s *Spec) { s.Entry = "" },
		"no summary":     func(s *Spec) { s.Summary = "" },
		"no target":      func(s *Spec) { s.Target = nil },
		"no inject":      func(s *Spec) { s.Inject = nil },
		"no reset":       func(s *Spec) { s.Reset = nil },
		"no firesWithin": func(s *Spec) { s.FiresWithin = 0 },
		"no names":       func(s *Spec) { s.Expect.Names = nil },
		"empty argv":     func(s *Spec) { s.Inject = []Command{{Run: nil}} },
		"quiet and allowed": func(s *Spec) {
			s.Expect.Quiet = []string{"x"}
			s.Expect.AllowFiring = []string{"x"}
		},
		"subject required quiet": func(s *Spec) { s.Expect.Quiet = []string{"subject"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := baseSpec()
			s.FiresWithin = Duration(time.Minute)
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("Validate accepted an unprovable spec (%s)", name)
			}
		})
	}
}

func TestValidSpecPasses(t *testing.T) {
	s := baseSpec()
	s.FiresWithin = Duration(time.Minute)
	if err := s.Validate(); err != nil {
		t.Errorf("Validate rejected a valid spec: %v", err)
	}
}

// A deferred write to a local that `return res` has already copied is
// invisible to the caller. The reset step and the total duration are both set
// in the defer, so without a named return a passing proof reports 0s and a
// failed reset is silently dropped from the result.
func TestDeferredResultFieldsReachTheCaller(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)

	res := r.Run(context.Background(), s)

	if res.Duration <= 0 {
		t.Errorf("duration = %v, want the elapsed time: the defer's write did not reach the caller", res.Duration)
	}
}

// A reset that fails must fail the proof, and that failure is recorded in the
// same defer. It is the more serious half of the same bug: a dirty testbed
// silently reported as green.
func TestFailedResetFailsTheProof(t *testing.T) {
	p := &fakeProm{values: map[string]float64{"subject": 5}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, p)
	r.Exec = func(_ context.Context, argv []string) error {
		if argv[0] == "reset" {
			return context.DeadlineExceeded
		}
		return nil
	}

	s := baseSpec()
	s.FiresWithin = Duration(3 * time.Minute)

	res := r.Run(context.Background(), s)

	if res.Passed {
		t.Fatal("proof passed although reset failed: the testbed is dirty for the next run")
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "RESET FAILED") {
		t.Errorf("failures = %v, want the reset failure recorded", res.Failures)
	}
}

// The bug a live run found: the resolution half used a second detector, which
// starts with an empty state machine. A resolution is only emitted on the
// firing-to-clear edge, so a detector that never saw the incident open can
// never see it close, and the proof blamed the entry for the prover's mistake.
//
// It hid because two entries passed anyway. With a short hold duration their
// fault outlived the reset long enough for the fresh detector to fire and then
// clear on its own. This fixture uses a long hold, where it cannot.
func TestResolutionUsesTheDetectorThatSawTheIncidentOpen(t *testing.T) {
	breaching := true
	p := &fakePromFunc{fn: func(promql string) (float64, error) {
		if strings.HasPrefix(promql, "count(") {
			return 1, nil
		}
		if breaching {
			return 5, nil
		}
		return 0, nil
	}}

	s := scenario("subject", 1)
	s.Signal.For = "120s" // long enough that a fresh detector cannot re-fire
	r, _ := harness(t, []catalog.Scenario{s}, nil)
	r.prom = p

	spec := baseSpec()
	spec.FiresWithin = Duration(6 * time.Minute)
	spec.ResolvesWithin = Duration(6 * time.Minute)
	spec.Expect.Names = []string{"payments"}
	// Reset clears the signal the moment it runs, exactly as a real reset does.
	r.Exec = func(_ context.Context, argv []string) error {
		if argv[0] == "reset" {
			breaching = false
		}
		return nil
	}

	res := r.Run(context.Background(), spec)

	if !res.Fired {
		t.Fatalf("entry did not fire: %v", res.Failures)
	}
	if !res.Resolved {
		t.Fatalf("incident opened but never closed: %v\n"+
			"a second detector cannot observe a lifecycle the first one started", res.Failures)
	}
	if !res.Passed {
		t.Errorf("proof failed: %v", res.Failures)
	}
}

// fakePromFunc drives every query through one function, so a test can flip the
// cluster from broken to healthy mid-proof.
type fakePromFunc struct{ fn func(string) (float64, error) }

func (f *fakePromFunc) Query(_ context.Context, promql string) (float64, error) {
	return f.fn(promql)
}

func (f *fakePromFunc) QuerySeries(_ context.Context, promql string) ([]prom.Sample, error) {
	v, err := f.fn(promql)
	if err != nil {
		return nil, err
	}
	return []prom.Sample{{Labels: map[string]string{"destination_workload": "payments-v2"}, Value: v}}, nil
}

// The failure this session actually hit: the Prometheus port-forward died,
// the harness queried a closed socket for seventeen minutes, and reported the
// catalog entry as failing. Blaming an entry for the observer's blindness is
// the exact mistake the detector's own four-state model exists to prevent,
// and a proof harness that commits it is worse than no harness.
func TestUnreachablePrometheusIsBlindNotAFailedEntry(t *testing.T) {
	p := &fakePromFunc{fn: func(string) (float64, error) {
		return 0, errors.New("dial tcp 127.0.0.1:9090: connect: connection refused")
	}}
	r, ran := harness(t, []catalog.Scenario{scenario("subject", 1)}, nil)
	r.prom = p

	s := baseSpec()
	s.Target = map[string]string{"service": "payments", "namespace": "demo"}
	s.FiresWithin = Duration(3 * time.Minute)

	res := r.Run(context.Background(), s)

	if !res.Blind {
		t.Fatalf("run was not marked blind: %+v", res.Failures)
	}
	if res.Passed {
		t.Error("a blind run must not pass either")
	}
	if !strings.Contains(res.BlindReason, "unreachable") {
		t.Errorf("blind reason = %q, want it to name the unreachable server", res.BlindReason)
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "says nothing about the entry") {
		t.Errorf("failures = %v, want them to refuse to blame the entry", res.Failures)
	}
	// And nothing was injected, so the cluster was never touched.
	for _, c := range *ran {
		if strings.Contains(c, "inject") {
			t.Errorf("a fault was injected despite the harness being blind: %v", *ran)
		}
	}
}

// A target that simply has no mesh telemetry is also blind, not a failure.
func TestTargetWithNoTelemetryIsBlind(t *testing.T) {
	p := &fakePromFunc{fn: func(string) (float64, error) { return 0, prom.ErrNoData }}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, nil)
	r.prom = p

	s := baseSpec()
	s.Target = map[string]string{"service": "ghost", "namespace": "nowhere"}
	s.FiresWithin = Duration(3 * time.Minute)

	res := r.Run(context.Background(), s)

	if !res.Blind || res.Passed {
		t.Fatalf("blind=%v passed=%v, want blind and not passed: %v", res.Blind, res.Passed, res.Failures)
	}
	if !strings.Contains(res.BlindReason, "no mesh telemetry") {
		t.Errorf("blind reason = %q", res.BlindReason)
	}
}

// Going blind partway through must also not be blamed on the entry: the fault
// was injected, the entry may well have fired, and the harness stopped being
// able to tell.
func TestGoingBlindMidRunIsNotAFailedEntry(t *testing.T) {
	alive := true
	p := &fakePromFunc{fn: func(promql string) (float64, error) {
		if !alive {
			return 0, errors.New("connection refused")
		}
		if strings.HasPrefix(promql, "count(") {
			return 1, nil
		}
		return 0, nil // never breaching while alive
	}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, nil)
	r.prom = p
	r.Exec = func(_ context.Context, argv []string) error {
		if argv[0] == "inject" {
			alive = false // the port-forward dies right after injection
		}
		return nil
	}

	s := baseSpec()
	s.Target = map[string]string{"service": "payments", "namespace": "demo"}
	s.FiresWithin = Duration(2 * time.Minute)

	res := r.Run(context.Background(), s)

	if !res.Blind {
		t.Fatalf("run was not marked blind after the observer died mid-run: %v", res.Failures)
	}
	if strings.Contains(strings.Join(res.Failures, " "), "did not fire within") {
		t.Errorf("the entry was blamed for the harness going blind: %v", res.Failures)
	}
}

// The third time the harness blamed the product for the environment. A laptop
// slept for nearly five hours while rate-limit-throttling was in its
// resolution window. The entry had already fired correctly in 1m3s. Go's
// monotonic clock stops during sleep, so the run measured nine minutes,
// concluded the incident never closed, and reported FAIL.
//
// Wall-clock is what notices; monotonic is exactly what cannot.
func TestSuspensionDuringResolutionIsBlindNotAFailedEntry(t *testing.T) {
	breaching := true
	p := &fakePromFunc{fn: func(promql string) (float64, error) {
		if strings.HasPrefix(promql, "count(") || promql == "vector(1)" {
			return 1, nil
		}
		if breaching {
			return 5, nil
		}
		return 0, nil
	}}

	sc := scenario("subject", 1)
	sc.Signal.For = ""
	r, _ := harness(t, []catalog.Scenario{sc}, nil)
	r.prom = p

	// The wall clock jumps five hours the moment the resolution phase starts,
	// exactly as a sleeping laptop does, while the monotonic clock does not.
	wall := time.Date(2026, 8, 19, 9, 30, 0, 0, time.UTC)
	jumped := false
	r.Wall = func() time.Time { return wall }
	_ = jumped
	r.Exec = func(_ context.Context, argv []string) error {
		if argv[0] == "reset" && !jumped {
			breaching = false
			jumped = true
			wall = wall.Add(4*time.Hour + 52*time.Minute)
		}
		return nil
	}

	s := baseSpec()
	s.Target = map[string]string{"service": "payments", "namespace": "demo"}
	s.Expect.Names = []string{"payments"}
	s.FiresWithin = Duration(3 * time.Minute)
	s.ResolvesWithin = Duration(6 * time.Minute)

	res := r.Run(context.Background(), s)

	if !res.Fired {
		t.Fatalf("the entry should still be recorded as having fired: %v", res.Failures)
	}
	if !res.Blind {
		t.Fatalf("a five-hour suspension was not detected: %v", res.Failures)
	}
	if res.Passed {
		t.Error("a void measurement must not pass either")
	}
	joined := strings.Join(res.Failures, " ")
	if strings.Contains(joined, "did not resolve within") {
		t.Errorf("the entry was blamed for the machine sleeping: %v", res.Failures)
	}
	if !strings.Contains(joined, "fired correctly") {
		t.Errorf("failures = %v, want them to record that the entry did its half", res.Failures)
	}
}

// An ordinary slow tick is not a suspension. Calling one blind would make the
// harness useless on a loaded machine.
func TestASlowTickIsNotASuspension(t *testing.T) {
	p := &fakePromFunc{fn: func(promql string) (float64, error) {
		if strings.HasPrefix(promql, "count(") || promql == "vector(1)" {
			return 1, nil
		}
		return 5, nil
	}}
	sc := scenario("subject", 1)
	sc.Signal.For = ""
	r, _ := harness(t, []catalog.Scenario{sc}, nil)
	r.prom = p

	wall := time.Date(2026, 8, 19, 9, 30, 0, 0, time.UTC)
	r.Wall = func() time.Time { wall = wall.Add(25 * time.Second); return wall } // slow, not frozen

	s := baseSpec()
	s.Target = map[string]string{"service": "payments", "namespace": "demo"}
	s.Expect.Names = []string{"payments"}
	s.FiresWithin = Duration(3 * time.Minute)

	res := r.Run(context.Background(), s)

	if res.Blind {
		t.Errorf("a 25s tick on a 10s poll was called a suspension: %s", res.BlindReason)
	}
	if !res.Passed {
		t.Errorf("proof failed: %v", res.Failures)
	}
}

// Measured live: the port-forward dropped for a single tick, the coverage
// probe failed, the detector cleared the pending breach as designed so that a
// breach measured while blind could not mature, the hold duration restarted
// with no budget left, and the run reported FAIL against an entry that had
// already been proven twice that day.
//
// One blink is enough to void a non-firing result. Re-checking liveness after
// the fact does not help: by then the observer has recovered and reports that
// everything is fine about a window it spent blind.
func TestOneFlickerVoidsANonFiringResult(t *testing.T) {
	tick := 0
	p := &fakePromFunc{fn: func(promql string) (float64, error) {
		if promql == "vector(1)" {
			tick++
			if tick == 3 { // one blink, then recovery
				return 0, errors.New("connection refused")
			}
			return 1, nil
		}
		if strings.HasPrefix(promql, "count(") {
			return 1, nil
		}
		return 0, nil // never breaching, so the entry cannot fire
	}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, nil)
	r.prom = p

	s := baseSpec()
	s.Target = map[string]string{"service": "payments", "namespace": "demo"}
	s.FiresWithin = Duration(2 * time.Minute)

	res := r.Run(context.Background(), s)

	if !res.Blind {
		t.Fatalf("a liveness flicker did not void the result: %v", res.Failures)
	}
	if res.Passed {
		t.Error("a blind run must not pass")
	}
	if strings.Contains(strings.Join(res.Failures, " "), "did not fire within") {
		t.Errorf("the entry was blamed for a network blink: %v", res.Failures)
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "not the entry") {
		t.Errorf("failures = %v, want them to name the network", res.Failures)
	}
}

// With no flicker, a genuinely non-firing entry must still fail. The rule
// above must not become a way for every failure to excuse itself.
func TestNoFlickerStillFailsANonFiringEntry(t *testing.T) {
	p := &fakePromFunc{fn: func(promql string) (float64, error) {
		if promql == "vector(1)" || strings.HasPrefix(promql, "count(") {
			return 1, nil
		}
		return 0, nil
	}}
	r, _ := harness(t, []catalog.Scenario{scenario("subject", 1)}, nil)
	r.prom = p

	s := baseSpec()
	s.Target = map[string]string{"service": "payments", "namespace": "demo"}
	s.FiresWithin = Duration(2 * time.Minute)

	res := r.Run(context.Background(), s)

	if res.Blind {
		t.Fatalf("a clean run was called blind: %s", res.BlindReason)
	}
	if res.Passed {
		t.Error("an entry that never fired must not pass")
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "did not fire within") {
		t.Errorf("failures = %v, want the entry blamed when the observer was fine", res.Failures)
	}
}

// A warm-up that starts while a previous fault is still decaying learns a
// normal several times too high, and an entry whose normal is wrong sleeps
// through the regression it was written for. Measured live: a run warmed on a
// cluster decaying from 488ms failed to fire at 486ms.
func TestWarmupLearnsFromTheFloorNotTheDecayingTail(t *testing.T) {
	inner := &countingStore{vals: map[string][]float64{}}
	g := &floorCollector{inner: inner, tolerance: 1.5}

	// A cluster decaying out of a previous fault, then settling at 50. The
	// sequence falls monotonically, which is exactly what defeats a
	// running-minimum filter: every reading is the lowest seen so far.
	for _, v := range []float64{488, 480, 242, 120, 55, 50, 48, 52, 49, 51} {
		g.Observe("k", v)
	}
	g.commit()

	got := inner.vals["k"]
	if len(got) == 0 {
		t.Fatal("nothing was learned at all")
	}
	for _, v := range got {
		if v > 100 {
			t.Errorf("learned %v from the decaying tail; the floor is around 50", v)
		}
	}
	if g.rejected == 0 {
		t.Error("nothing was rejected, so the gate did nothing")
	}
	t.Logf("accepted %d, rejected %d, learned %v", g.accepted, g.rejected, got)
}

// A cluster that is quiet from the start must not have its readings thrown
// away: the gate is a guard against a decaying start, not a filter that only
// likes the very first number.
func TestWarmupAcceptsAQuietClusterThroughout(t *testing.T) {
	inner := &countingStore{vals: map[string][]float64{}}
	g := &floorCollector{inner: inner, tolerance: 1.5}

	for _, v := range []float64{50, 52, 48, 55, 47, 51, 53, 49} {
		g.Observe("k", v)
	}
	g.commit()
	if g.rejected != 0 {
		t.Errorf("rejected %d readings from an already-quiet cluster", g.rejected)
	}
	if len(inner.vals["k"]) != 8 {
		t.Errorf("learned %d readings, want all 8", len(inner.vals["k"]))
	}
}

type countingStore struct{ vals map[string][]float64 }

func (c *countingStore) Observe(key string, v float64) { c.vals[key] = append(c.vals[key], v) }
func (c *countingStore) Baseline(key string, min int) (float64, bool) {
	vs := c.vals[key]
	if len(vs) < min {
		return 0, false
	}
	var sum float64
	for _, v := range vs {
		sum += v
	}
	return sum / float64(len(vs)), true
}
