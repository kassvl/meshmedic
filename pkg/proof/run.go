package proof

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/kassvl/meshmedic/pkg/baseline"
	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
	"github.com/kassvl/meshmedic/pkg/remediate"
	"github.com/kassvl/meshmedic/pkg/report"
)

// Result is the outcome of one entry's proof.
type Result struct {
	Entry    string
	Passed   bool
	Duration time.Duration

	Fired        bool
	FiredAfter   time.Duration
	Named        []string // expected names that appeared in the report
	Missing      []string // expected names that did not
	Unexpected   []string // other entries that fired and were not allowed to
	Resolved     bool
	ResolveAfter time.Duration

	// Blind means the harness could not observe the cluster, so the run says
	// nothing about the entry. It is deliberately not the same as Passed
	// being false: blaming an entry for the observer's blindness is the exact
	// failure the detector's own four-state model exists to prevent, and a
	// proof harness that commits it is worse than no harness.
	Blind       bool
	BlindReason string

	// Failures are the specific reasons the proof did not pass, in the words
	// an operator needs to fix either the entry or the proof.
	Failures []string
	// Report is the incident document the entry produced, kept so a passing
	// proof is auditable rather than merely green.
	Report string
}

// Runner executes proofs against a live cluster.
type Runner struct {
	scenarios []catalog.Scenario
	// prom is the querier; exported through the constructor and overridable in
	// tests that need to change the cluster mid-proof.
	prom detect.Querier
	// Exec runs an injection or reset command. Injected so tests can prove
	// the runner's logic without touching a cluster.
	Exec func(ctx context.Context, argv []string) error
	// Now is the clock, injected for the same reason.
	Now func() time.Time
	// Log receives progress.
	Log func(format string, args ...any)
	// Poll is how often the detector is ticked while waiting.
	Poll time.Duration
	// Sleep waits, injected so tests do not.
	Sleep func(context.Context, time.Duration)

	// Baseline lets a relative-threshold entry be proven. Without one the
	// detector falls back to the static threshold, and for
	// latency-regression-vs-baseline that means the fault has to exceed 1000ms
	// to fire, which also puts canary-latency-rollback in breach, which
	// suppresses the very entry under proof. The only honest proof of a
	// relative threshold warms a baseline first and then fires below the
	// static fallback.
	Baseline detect.BaselineStore

	// Objects and Triage give the detector the same cluster reads the CLI
	// wires. Nil means configuration, log and rollout evidence are skipped,
	// which silently weakens every proof, so NewRunner sets them and the
	// command fails loudly when it cannot.
	Objects detect.ObjectReader
	Triage  detect.TriageReader

	// Wall reads the wall clock, which unlike Now keeps advancing while the
	// machine is asleep. The difference between the two is how a suspended
	// run is detected.
	Wall func() time.Time
	// SuspendAfter is the wall-clock gap between ticks that means the process
	// was suspended rather than merely slow. Zero uses ten times Poll, with a
	// floor of two minutes.
	SuspendAfter time.Duration
	// suspended records the gap when one is detected.
	suspended time.Duration
	// flickers counts liveness failures during a run. Even one matters: a
	// five-second outage fails the coverage probe, which by design clears any
	// pending breach so that a breach measured while blind cannot mature into
	// an incident. That is correct for the detector and brutal for a fixed
	// proof budget, because a blink costs the entry its entire hold duration.
	// So a run that flickered and then did not fire is not evidence about the
	// entry; it is evidence about the network.
	flickers int

	// onIncident is swapped between the firing and resolving phases so both
	// can share one detector, and therefore one state machine.
	onIncident func(detect.Incident)
}

// suspendThreshold is the wall gap that means suspension. Generous on purpose:
// a slow tick is normal, a two-minute gap between ten-second polls is not.
func (r *Runner) suspendThreshold() time.Duration {
	if r.SuspendAfter > 0 {
		return r.SuspendAfter
	}
	if t := 10 * r.Poll; t > 2*time.Minute {
		return t
	}
	return 2 * time.Minute
}

// NewRunner builds a runner. reader supplies the cluster reads the detector
// needs for configuration, log and rollout evidence; passing nil produces
// proofs that cannot see any of it.
func NewRunner(scenarios []catalog.Scenario, q detect.Querier, reader *kube.Reader) *Runner {
	r := &Runner{
		scenarios: scenarios,
		prom:      q,
		Exec:      execCommand,
		Now:       time.Now,
		Log:       func(string, ...any) {},
		Poll:      10 * time.Second,
		Sleep:     sleepCtx,
		Wall:      func() time.Time { return time.Now().Round(0) },
	}
	if reader != nil {
		r.Objects = reader
		r.Triage = reader
	}
	return r
}

func execCommand(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Run executes one proof end to end. Reset always runs, including on failure
// and on a cancelled context, because a proof that leaves its fault behind
// poisons every run after it.
//
// The guarantee has one hole worth naming, because it bit: reset is a
// deferred call, and a deferred call does not run when the process is killed
// outright. A proof interrupted by SIGKILL, or by a shell timeout that kills
// rather than signals, leaves its fault applied. Ctrl-C and SIGTERM are fine;
// the command handles both and resets.
//
// Nothing in this package can close that hole, so the preflight closes it from
// the other side instead: the next run refuses to start while any entry is
// already breaching, which catches a leftover fault of any shape, including
// the env-var kind that leaves no object behind for a resource check to find.
func (r *Runner) Run(ctx context.Context, s Spec) (res Result) {
	start := r.Now()
	wallStart := r.Wall()
	r.suspended = 0
	r.flickers = 0
	res = Result{Entry: s.Entry}

	defer func() {
		r.Log("resetting")
		// Reset gets its own context: cancelling the run must not leave the
		// cluster broken.
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
		defer cancel()
		for _, c := range s.Reset {
			if err := r.Exec(resetCtx, c.Run); err != nil {
				res.Failures = append(res.Failures,
					fmt.Sprintf("RESET FAILED (%s): %v — the testbed may be dirty for the next proof", c, err))
				res.Passed = false
			}
		}
		res.Duration = r.Now().Sub(start)

		// The whole-run detector, and the one that actually catches a
		// sleeping laptop. Go's monotonic clock stops while the machine is
		// suspended, so a run frozen for five hours reports nine minutes of
		// elapsed time and every deadline inside it silently expired without
		// a single observation. Wall clock is the only witness. Compared at
		// the end, this catches a suspension anywhere in the run, including
		// during a kubectl call where no tick loop is watching.
		if wallElapsed := r.Wall().Sub(wallStart); wallElapsed-res.Duration > r.suspendThreshold() {
			gap := wallElapsed - res.Duration
			if gap > r.suspended {
				r.suspended = gap
			}
			res.Blind = true
			res.BlindReason = fmt.Sprintf("the process was suspended for about %s during the run", gap.Round(time.Second))
			res.Passed = false
			note := "BLIND: " + res.BlindReason + " — the measurement is void and says nothing about the entry"
			if res.Fired {
				note = "BLIND: " + res.BlindReason + " — the entry fired correctly in " +
					res.FiredAfter.Round(time.Second).String() + "; the rest of the measurement is void"
			}
			res.Failures = append([]string{note}, res.Failures...)
		}
	}()

	// Preflight before touching the cluster. A harness that cannot reach
	// Prometheus will watch nothing happen and call the entry broken, which
	// is what a dead port-forward did here: seventeen minutes of queries into
	// a closed socket, reported as a failing catalog entry.
	if reason, ok := r.preflight(ctx, s); !ok {
		res.Blind, res.BlindReason = true, reason
		res.Failures = append(res.Failures,
			"BLIND: "+reason+" — this run says nothing about the entry, and no fault was injected")
		return res
	}

	// Warm-up, before the fault. A relative threshold cannot be proven against
	// a cold baseline: the detector falls back to the static number, and for
	// the latency entry that number is high enough that firing it also fires
	// the canary entry, which suppresses it.
	if s.Warmup.D() > 0 {
		if r.Baseline == nil {
			r.Baseline = baseline.New("", 0.05)
		}
		// Learn from the floor, not from whatever the cluster happened to be
		// doing when warm-up started. A previous proof's fault decays out of a
		// two-minute rate window slowly, and the preflight cannot catch it
		// because a decaying 480ms is still far under a 1000ms static
		// threshold. Warming on that tail teaches the entry a normal three
		// times too high, and it then sleeps through the very regression it
		// exists to catch. Measured: a run warmed on a decaying cluster failed
		// to fire at 486ms.
		//
		// So the warm-up watches the signal settle and only trusts readings
		// near the lowest it has seen. The floor is what healthy means.
		r.Log("warming the baseline for %s, learning from the signal's floor", s.Warmup.D())
		warm := r.detectorFor(s)
		gate := &floorCollector{inner: r.Baseline, tolerance: 1.5}
		warm.Baseline = gate
		r.onIncident = func(detect.Incident) {}
		deadline := r.Now().Add(s.Warmup.D())
		for r.Now().Before(deadline) && ctx.Err() == nil {
			warm.Tick(ctx, r.Now())
			r.Sleep(ctx, r.Poll)
		}
		gate.commit()
		r.Log("baseline warmed: %d readings near the floor accepted, %d rejected as still-decaying", gate.accepted, gate.rejected)
	}

	r.Log("injecting the fault")
	for _, c := range s.Inject {
		if err := r.Exec(ctx, c.Run); err != nil {
			res.Failures = append(res.Failures, fmt.Sprintf("inject failed (%s): %v", c, err))
			return res
		}
	}

	if s.Settle.D() > 0 {
		r.Log("settling for %s so rate windows fill", s.Settle.D())
		r.Sleep(ctx, s.Settle.D())
	}

	// One detector for the whole proof. A resolution report is only emitted on
	// the firing-to-clear edge, so a second detector built for the resolution
	// phase starts with an empty state machine, never sees the incident open,
	// and can never see it close. Two entries passed anyway, by accident: with
	// a 60s hold their fault outlived the reset long enough for the fresh
	// detector to fire and then clear on its own. error-surge's 120s hold did
	// not, and the proof blamed the entry for the prover's mistake.
	d := r.detectorFor(s)
	fired, others, doc, after := r.waitForFire(ctx, d, s)
	res.Fired = fired
	res.FiredAfter = after
	res.Report = doc

	if r.suspended > 0 {
		res.Blind = true
		res.BlindReason = fmt.Sprintf("the process was suspended for %s mid-run", r.suspended.Round(time.Second))
		res.Failures = append(res.Failures,
			"BLIND: "+res.BlindReason+" — the measurement is void and says nothing about the entry")
		return res
	}
	if !fired {
		// A flicker is enough. Checking preflight again here is not, because
		// by the time an entry has failed to fire the observer has usually
		// recovered, and a recovered observer reports everything is fine
		// about a window it spent blind. Measured live: the port-forward
		// dropped for one tick, the coverage probe failed, the pending
		// breach was cleared as designed, the hold duration restarted with
		// no budget left, and the run blamed an entry that had already been
		// proven twice.
		if r.flickers > 0 {
			res.Blind = true
			res.BlindReason = fmt.Sprintf("the observer went blind %d time(s) during the window", r.flickers)
			res.Failures = append(res.Failures,
				"BLIND: "+res.BlindReason+" — each blink clears any pending breach by design, so a non-firing result here is evidence about the network, not the entry")
			return res
		}
		// Distinguish "the entry did not fire" from "we could not see". If
		// the observation went blind partway through, the entry is not the
		// thing that failed.
		if reason, ok := r.preflight(ctx, s); !ok {
			res.Blind, res.BlindReason = true, reason
			res.Failures = append(res.Failures,
				"BLIND: "+reason+" — the fault was injected but the harness stopped being able to observe, so this says nothing about the entry")
			return res
		}
		res.Failures = append(res.Failures, fmt.Sprintf(
			"%s did not fire within %s of the fault settling", s.Entry, s.FiresWithin.D()))
		return res
	}
	r.Log("%s fired after %s", s.Entry, after.Round(time.Second))

	// Naming. An entry that fires without naming the culprit has detected
	// something and explained nothing, which is the failure mode a bare
	// "did it fire" assertion cannot see.
	for _, want := range s.Expect.Names {
		if strings.Contains(doc, want) {
			res.Named = append(res.Named, want)
			continue
		}
		res.Missing = append(res.Missing, want)
	}
	if len(res.Missing) > 0 {
		res.Failures = append(res.Failures, fmt.Sprintf(
			"the report does not name %s: the entry fired but did not explain what to act on",
			strings.Join(res.Missing, ", ")))
	}

	// Neighbours. Anything that fired and was neither the subject nor
	// explicitly allowed is either a cascade the entry should have
	// suppressed, or evidence the fault was less isolated than claimed.
	allowed := map[string]bool{s.Entry: true}
	for _, id := range s.Expect.AllowFiring {
		allowed[id] = true
	}
	quiet := map[string]bool{}
	for _, id := range s.Expect.Quiet {
		quiet[id] = true
	}
	for _, id := range others {
		if allowed[id] {
			continue
		}
		res.Unexpected = append(res.Unexpected, id)
		switch {
		case quiet[id]:
			res.Failures = append(res.Failures, fmt.Sprintf(
				"%s fired but was required to stay quiet: the suppression this entry claims is not working", id))
		default:
			res.Failures = append(res.Failures, fmt.Sprintf(
				"%s fired alongside it and no one said it would: either the fault is less isolated than the proof claims, or that entry is miscalibrated", id))
		}
	}

	if s.ResolvesWithin.D() > 0 {
		r.Log("resetting to prove the incident closes")
		for _, c := range s.Reset {
			if err := r.Exec(ctx, c.Run); err != nil {
				res.Failures = append(res.Failures, fmt.Sprintf("reset failed during the resolution half (%s): %v", c, err))
				return res
			}
		}
		resolved, rAfter := r.waitForResolve(ctx, d, s)
		res.Resolved = resolved
		res.ResolveAfter = rAfter
		if r.suspended > 0 {
			res.Blind = true
			res.BlindReason = fmt.Sprintf("the process was suspended for %s during the resolution window", r.suspended.Round(time.Second))
			res.Failures = append(res.Failures,
				"BLIND: "+res.BlindReason+" — the entry fired correctly; only the resolution half is void")
			return res
		}
		if !resolved {
			res.Failures = append(res.Failures, fmt.Sprintf(
				"%s did not resolve within %s of the fault being removed: the incident opens but never closes",
				s.Entry, s.ResolvesWithin.D()))
		} else {
			r.Log("%s resolved after %s", s.Entry, rAfter.Round(time.Second))
		}
	}

	res.Passed = len(res.Failures) == 0
	return res
}

// alive is the cheap per-tick liveness check: is the server still answering?
// It deliberately does not ask about the target, only about sight, so a fault
// that legitimately drives a target's telemetry to zero is not mistaken for a
// dead observer.
func (r *Runner) alive(ctx context.Context) (string, bool) {
	if _, err := r.prom.Query(ctx, "vector(1)"); err != nil && !errors.Is(err, prom.ErrNoData) {
		return "Prometheus is not answering: " + err.Error(), false
	}
	return "", true
}

// preflight asks whether the harness can observe this target at all: is
// Prometheus answering, and does the target resolve to mesh telemetry. It runs
// before injection and again whenever an entry fails to fire, so a blind run
// is reported as blind rather than as a broken entry.
func (r *Runner) preflight(ctx context.Context, s Spec) (string, bool) {
	probe, err := renderQuery("preflight", detect.DefaultCoverageProbe, s.Target)
	if err != nil {
		// No namespace to probe on: fall back to asking whether the server
		// answers at all, which is the part that actually breaks.
		probe = "up"
	}
	samples, err := r.prom.QuerySeries(ctx, probe)
	switch {
	case errors.Is(err, prom.ErrNoData), err == nil && len(samples) == 0:
		return "the target produces no mesh telemetry, so nothing this proof does could be observed", false
	case err != nil:
		return "Prometheus is unreachable: " + err.Error(), false
	}
	return "", true
}

// waitForFire ticks a detector until the subject fires or the budget runs out,
// collecting which other entries fired along the way.
func (r *Runner) waitForFire(ctx context.Context, d *detect.Detector, s Spec) (fired bool, others []string, doc string, after time.Duration) {
	seen := map[string]bool{}
	start := r.Now()
	deadline := start.Add(s.FiresWithin.D())

	r.onIncident = func(inc detect.Incident) {
		if inc.Scenario.ID == s.Entry && doc == "" {
			patch, err := remediate.Render(inc.Scenario, inc.Params)
			if err != nil {
				patch = "# patch rendering failed\n"
			}
			doc = report.Markdown(inc, patch)
			fired = true
			after = r.Now().Sub(start)
			return
		}
		if inc.Scenario.ID != s.Entry && !seen[inc.Scenario.ID] {
			seen[inc.Scenario.ID] = true
		}
	}

	// Watch the observer, not just the subject. A harness that only checks
	// its own sight at the start will happily burn its entire budget querying
	// a socket that closed thirty seconds in, then blame the entry. Three
	// consecutive failed liveness checks ends the run in seconds.
	consecutiveBlind := 0
	lastTick := r.Wall()
	for r.Now().Before(deadline) {
		// A suspended observer is a blind one. Go's monotonic clock stops
		// while a laptop sleeps, so a run that was frozen for five hours
		// measures nine minutes and reports the entry as never having
		// resolved. That happened here: rate-limit-throttling fired
		// correctly in 1m3s, the machine slept through the resolution
		// window, and the harness blamed the entry. Wall-clock is what
		// notices; monotonic is exactly what cannot.
		if gap := r.Wall().Sub(lastTick); gap > r.suspendThreshold() {
			r.Log("wall clock jumped %s between ticks: the process was suspended, so this measurement is void", gap.Round(time.Second))
			r.suspended = gap
			return false, nil, "", 0
		}
		lastTick = r.Wall()

		d.Tick(ctx, r.Now())
		if fired {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if _, ok := r.alive(ctx); ok {
			consecutiveBlind = 0
		} else {
			consecutiveBlind++
			r.flickers++
			r.Log("liveness check %d/3 failed (%d this run): the observer may have died", consecutiveBlind, r.flickers)
			if consecutiveBlind >= 3 {
				r.Log("aborting: three consecutive liveness failures, the harness is blind")
				return false, nil, "", 0
			}
		}
		r.Sleep(ctx, r.Poll)
	}
	// Keep ticking briefly after the subject fires so a neighbour that fires
	// a moment later is still caught. A cascade that shows up ten seconds
	// after the subject is exactly the case suppression is meant to handle.
	if fired {
		grace := r.Now().Add(30 * time.Second)
		for r.Now().Before(grace) && ctx.Err() == nil {
			d.Tick(ctx, r.Now())
			r.Sleep(ctx, r.Poll)
		}
	}

	for id := range seen {
		others = append(others, id)
	}
	sort.Strings(others)
	return fired, others, doc, after
}

// waitForResolve ticks until the subject emits a resolution.
func (r *Runner) waitForResolve(ctx context.Context, d *detect.Detector, s Spec) (bool, time.Duration) {
	start := r.Now()
	deadline := start.Add(s.ResolvesWithin.D())
	resolved := false

	// Same detector, so the incident this proof just watched open is the one
	// it now watches close.
	r.onIncident = func(detect.Incident) {}
	d.OnResolve = func(_ context.Context, res detect.Resolution) error {
		if res.Scenario.ID == s.Entry {
			resolved = true
		}
		return nil
	}
	lastTick := r.Wall()
	for r.Now().Before(deadline) && ctx.Err() == nil {
		if gap := r.Wall().Sub(lastTick); gap > r.suspendThreshold() {
			r.Log("wall clock jumped %s between ticks: the process was suspended, so this measurement is void", gap.Round(time.Second))
			r.suspended = gap
			return false, 0
		}
		lastTick = r.Wall()

		d.Tick(ctx, r.Now())
		if resolved {
			return true, r.Now().Sub(start)
		}
		r.Sleep(ctx, r.Poll)
	}
	return false, r.Now().Sub(start)
}

// detectorFor builds the single detector a proof uses for both phases. Its
// handler forwards to whatever r.onIncident currently is, which lets the two
// phases install different behaviour without replacing the state machine.
func (r *Runner) detectorFor(s Spec) *detect.Detector {
	d := detect.New(r.scenarios,
		[]detect.Target{{Params: s.Target}},
		r.prom,
		func(_ context.Context, inc detect.Incident) error {
			if r.onIncident != nil {
				r.onIncident(inc)
			}
			return nil
		})
	// Surface the detector's own diagnostics. Discarding them threw away the
	// one line that explains a non-firing entry: latency-regression-vs-baseline
	// failed a proof and the reason, "suppressed by canary-latency-rollback",
	// was printed by the detector and swallowed here.
	d.Log = func(format string, args ...any) { r.Log("detector: "+format, args...) }
	d.Now = r.Now
	// The prover must build the same detector the CLI does, or a proof is
	// weaker than the thing it claims to prove. Without a cluster reader the
	// detector silently skips configuration, log and rollout evidence, so a
	// triage entry whose entire value is the client's own failure line and the
	// rollout diff that caused it can fire, produce a dossier with none of it,
	// and be marked as passing on metric evidence alone.
	d.Objects = r.Objects
	d.Triage = r.Triage
	d.Baseline = r.Baseline
	return d
}

// renderQuery fills a query template with the proof's target parameters.
func renderQuery(name, promql string, params map[string]string) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(promql)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// floorCollector gathers warm-up readings without teaching them to anything,
// then feeds the real store only the ones near the window's floor.
//
// Two passes, and the reason is a bug the one-pass version had: a cluster
// decaying out of a previous fault produces a monotonically falling sequence,
// so every reading is the lowest seen so far and a running-minimum filter
// accepts all of them. The floor is only knowable once the window is over.
//
// The floor is what healthy means. Warming on the tail teaches a normal
// several times too high, and an entry whose normal is wrong sleeps through
// the regression it exists to catch: measured live, a run warmed on a cluster
// decaying from 488ms then failed to fire at 486ms.
type floorCollector struct {
	inner              detect.BaselineStore
	tolerance          float64
	seen               map[string][]float64
	accepted, rejected int
}

func (c *floorCollector) Observe(key string, value float64) {
	if c.seen == nil {
		c.seen = map[string][]float64{}
	}
	c.seen[key] = append(c.seen[key], value)
}

// Baseline during collection reports not-ready, so the detector keeps using
// the static threshold and keeps observing rather than deciding on a normal
// that has not been established yet.
func (c *floorCollector) Baseline(string, int) (float64, bool) { return 0, false }

// commit teaches the real store the readings near each key's floor.
func (c *floorCollector) commit() {
	for key, vals := range c.seen {
		floor := math.Inf(1)
		for _, v := range vals {
			if v < floor {
				floor = v
			}
		}
		for _, v := range vals {
			if floor > 0 && v > floor*c.tolerance {
				c.rejected++
				continue
			}
			c.accepted++
			c.inner.Observe(key, v)
		}
	}
}
