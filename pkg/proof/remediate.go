package proof

import (
	"context"
	"fmt"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/remediate"
)

// A remediation proof asks the question an operator actually has.
//
// Every spec in proof/ shows that the right entry fired and named the culprit.
// None of them show that merging what it proposed would have helped, and
// "opens the fix as a pull request" is the sentence this project leads with.
// It is the largest unverified claim in the product.
//
// The shape that makes it a real test is applying the patch while the fault is
// still in place. Removing the fault and watching the signal fall proves the
// fault caused it, which the resolution half of the ordinary proof already
// covers. Leaving the fault and applying the fix asks: if I merge this, does
// the incident stop?
//
// It is a separate command rather than another step in the ordinary proof
// because the two would collide. The resolution half watches for the
// firing-to-clear edge after the fault is reset; a patch that works produces
// that edge earlier, so the ordinary proof would end up timing the remediation
// and calling it the reset. Two claims, two runs, neither borrowing the
// other's evidence.

// RemediationResult is what applying an entry's own patch proved.
type RemediationResult struct {
	Entry string `json:"entry"`

	// Skipped explains why no remediation was attempted, and is the whole
	// result when it is set. Thirteen of eighteen entries are report-only:
	// they produce a dossier and propose nothing, so there is nothing here to
	// prove and saying so is not a gap.
	Skipped string `json:"skipped,omitempty"`

	Fired    bool `json:"fired"`
	Applied  bool `json:"applied"`
	Cleared  bool `json:"cleared"`
	Reverted bool `json:"reverted"`
	// Patch is the YAML actually applied, kept so a passing proof is auditable
	// rather than merely green.
	Patch string `json:"patch,omitempty"`

	// AtFire is the signal when the incident was reported and AtEnd is where
	// it finished. Threshold is what both are measured against.
	AtFire       float64       `json:"at_fire"`
	AtEnd        float64       `json:"at_end"`
	Threshold    float64       `json:"threshold"`
	ClearedAfter time.Duration `json:"cleared_after"`
	Duration     time.Duration `json:"duration"`

	Blind       bool     `json:"blind"`
	BlindReason string   `json:"blind_reason,omitempty"`
	Failures    []string `json:"failures,omitempty"`
}

// Passed reports whether the entry's own remedy stopped its own incident.
func (r RemediationResult) Passed() bool {
	return r.Skipped == "" && !r.Blind && r.Cleared && len(r.Failures) == 0
}

// Verdict is the word for the summary table.
func (r RemediationResult) Verdict() string {
	switch {
	case r.Skipped != "":
		return "skipped"
	case r.Blind:
		return "BLIND"
	case r.Passed():
		return "pass"
	default:
		return "FAIL"
	}
}

// Remediate proves that the patch an entry proposes stops the incident it
// reported, with the fault still in place.
//
// The return value is named because the deferred undo writes to it. With an
// unnamed return, `return res` copies the struct into the return slot before
// any defer runs, so everything the cleanup records, whether the revert
// succeeded, how long the run took, any failure while undoing, is written to a
// variable nobody reads. A test caught three such losses at once.
func (r *Runner) Remediate(ctx context.Context, s Spec) (res RemediationResult) {
	res = RemediationResult{Entry: s.Entry}
	start := r.Now()
	defer func() { res.Duration = r.Now().Sub(start) }()

	entry, ok := r.scenario(s.Entry)
	if !ok {
		res.Failures = append(res.Failures, "no catalog entry with this id")
		return res
	}
	if s.Remediation == nil {
		res.Skipped = "the spec declares no remediation check"
		return res
	}
	if entry.Remediation.Action == "report-only" || entry.Remediation.PatchTemplate == "" {
		res.Skipped = "the entry is report-only: it proposes a dossier, not a patch, so there is no fix to prove"
		return res
	}

	patch, err := remediate.Render(entry, s.Target)
	if err != nil {
		res.Failures = append(res.Failures, "the entry's patch does not render: "+err.Error())
		return res
	}
	res.Patch = patch

	if reason, ok := r.preflight(ctx, s); !ok {
		res.Blind, res.BlindReason = true, reason
		res.Failures = append(res.Failures,
			"BLIND: "+reason+". Nothing was injected, and this says nothing about the remedy")
		return res
	}

	// Undo in the reverse order things were done, and always. A run that
	// leaves its own remediation behind has quietly reconfigured the mesh for
	// every measurement after it, which is worse than leaving a fault: a fault
	// is loud and a fix is not.
	defer func() {
		for _, c := range s.Remediation.Revert {
			if err := r.Exec(context.WithoutCancel(ctx), c.Run); err != nil {
				res.Failures = append(res.Failures, fmt.Sprintf("revert failed (%s): %v", c, err))
				return
			}
		}
		res.Reverted = true
		for _, c := range s.Reset {
			if err := r.Exec(context.WithoutCancel(ctx), c.Run); err != nil {
				res.Failures = append(res.Failures, fmt.Sprintf("reset failed (%s): %v", c, err))
				return
			}
		}
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

	d := r.detectorFor(s)
	threshold, _ := d.EffectiveThreshold(entry, detect.Target{Params: s.Target})
	res.Threshold = threshold

	// Wait for the signal to be in breach, because a remedy applied to a
	// cluster that is fine proves nothing about the entry.
	//
	// Breach, not a reported incident: the entry reports once its hold
	// elapses, and waiting that out would delay the patch by minutes without
	// changing what is being proven. The patch is rendered from the entry and
	// the target, which is exactly what the report renders it from, so the
	// bytes applied here are the bytes an operator would have merged.
	fired, value := r.waitForBreach(ctx, d, entry, s, s.FiresWithin.D())
	res.Fired, res.AtFire = fired, value
	if !fired {
		res.Failures = append(res.Failures, fmt.Sprintf(
			"the entry never fired within %s, so there was no incident to remedy", s.FiresWithin.D()))
		return res
	}
	r.Log("%s fired at %.4g; applying the patch it proposes, with the fault still in place", s.Entry, value)

	apply := r.ExecInput
	if apply == nil {
		apply = execCommandInput
	}
	if err := apply(ctx, r.applyArgv(s), patch); err != nil {
		res.Failures = append(res.Failures, "applying the proposed patch failed: "+err.Error())
		return res
	}
	res.Applied = true

	cleared, after, end := r.waitForRecovery(ctx, d, entry, s, s.Remediation.ClearsWithin.D())
	res.Cleared, res.ClearedAfter, res.AtEnd = cleared, after, end
	if !cleared {
		res.Failures = append(res.Failures, fmt.Sprintf(
			"the signal was still at %.4g against a threshold of %.4g %s after the patch was applied: "+
				"the entry reported an incident and proposed a change that did not stop it",
			end, threshold, s.Remediation.ClearsWithin.D()))
		return res
	}
	r.Log("the signal fell to %.4g after %s, with the fault untouched", end, after.Round(time.Second))
	return res
}

// applyArgv is how the patch reaches the cluster. It reuses the context the
// spec's own commands pin, so a remediation cannot land somewhere the fault
// did not.
func (r *Runner) applyArgv(s Spec) []string {
	argv := []string{"kubectl"}
	for _, c := range s.Inject {
		for _, a := range c.Run {
			if len(a) > 10 && a[:10] == "--context=" {
				argv = append(argv, a)
			}
		}
		break
	}
	return append(argv, "apply", "-f", "-")
}

// waitForBreach ticks until the entry's signal is over its threshold, and
// reports the value it reached.
func (r *Runner) waitForBreach(ctx context.Context, d *detect.Detector, entry catalog.Scenario, s Spec, budget time.Duration) (bool, float64) {
	deadline := r.Now().Add(budget)
	var last float64
	for r.Now().Before(deadline) && ctx.Err() == nil {
		cycle := d.Tick(ctx, r.Now())
		for _, ev := range cycle.Evaluations {
			if ev.Scenario != entry.ID {
				continue
			}
			last = ev.Value
			if ev.Outcome == detect.OutcomeFiring {
				return true, ev.Value
			}
		}
		r.Sleep(ctx, r.Poll)
	}
	return false, last
}

// waitForRecovery ticks until the signal is back under its threshold.
func (r *Runner) waitForRecovery(ctx context.Context, d *detect.Detector, entry catalog.Scenario, s Spec, budget time.Duration) (bool, time.Duration, float64) {
	start := r.Now()
	deadline := start.Add(budget)
	var last float64
	for r.Now().Before(deadline) && ctx.Err() == nil {
		cycle := d.Tick(ctx, r.Now())
		for _, ev := range cycle.Evaluations {
			if ev.Scenario != entry.ID {
				continue
			}
			last = ev.Value
			if ev.Outcome == detect.OutcomeClear {
				return true, r.Now().Sub(start), ev.Value
			}
		}
		r.Sleep(ctx, r.Poll)
	}
	return false, r.Now().Sub(start), last
}

// scenario finds a catalog entry by id.
func (r *Runner) scenario(id string) (catalog.Scenario, bool) {
	for _, s := range r.scenarios {
		if s.ID == id {
			return s, true
		}
	}
	return catalog.Scenario{}, false
}
