package proof

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
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

	// onIncident is swapped between the firing and resolving phases so both
	// can share one detector, and therefore one state machine.
	onIncident func(detect.Incident)
}

// NewRunner builds a runner with real command execution and a real clock.
func NewRunner(scenarios []catalog.Scenario, q detect.Querier) *Runner {
	return &Runner{
		scenarios: scenarios,
		prom:      q,
		Exec:      execCommand,
		Now:       time.Now,
		Log:       func(string, ...any) {},
		Poll:      10 * time.Second,
		Sleep:     sleepCtx,
	}
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
func (r *Runner) Run(ctx context.Context, s Spec) (res Result) {
	start := r.Now()
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
	}()

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

	if !fired {
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

	for r.Now().Before(deadline) {
		d.Tick(ctx, r.Now())
		if fired {
			break
		}
		if ctx.Err() != nil {
			break
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
	for r.Now().Before(deadline) && ctx.Err() == nil {
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
	d.Log = func(string, ...any) {}
	d.Now = r.Now
	return d
}
