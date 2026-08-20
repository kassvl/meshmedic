package proof

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/kassvl/meshmedic/pkg/baseline"
	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
)

// Silence is the other half of the proof suite, and it exists because the
// first half cannot ask this question.
//
// Every spec in proof/ says: inject this fault, and the entry must fire. Run
// the whole directory and you have shown that the catalog recognises eighteen
// failures. You have shown nothing about what it does on a Tuesday, and a
// detector that fires on everything passes all of it.
//
// The gap is not theoretical. On 2026-08-19 an entry entered breach on a
// cluster with nothing injected into its path and stayed there for 180
// unbroken seconds, twice its hold duration; the only reason it did not
// publish an incident about a dependency that was never slow is that the
// observation window closed 25 seconds in. Nothing in the suite was looking
// for that. It was found by reading a benchmark run afterwards.
//
// `meshmedic calibrate` is a different question and does not close this. It
// samples the signal and reports how far the healthy extreme sits from the
// threshold. This runs the real detector: hold durations, suppression, the
// coverage probe, the four-state model, the guardrails. Calibrate asks how
// close the number is. This asks whether anyone would have been paged.

// SilenceVerdict is what watching a healthy cluster proved about one entry.
type SilenceVerdict string

const (
	// Silent: the signal never crossed its threshold. The strongest result.
	Silent SilenceVerdict = "silent"
	// Brushed: it crossed, but never for long enough to be reported. The
	// entry behaved correctly and the margin it did so on is the interesting
	// number, because that margin is what a busier hour eats into.
	Brushed SilenceVerdict = "brushed"
	// Fired: an incident was reported on a cluster where nothing was
	// injected. A false positive, measured rather than argued about.
	Fired SilenceVerdict = "FIRED"
	// Unwatched: no reading resolved for this entry, so its silence says
	// nothing about it. Distinct from Silent for the same reason blind is
	// distinct from clear everywhere else in this project.
	Unwatched SilenceVerdict = "unwatched"
	// NotApplicable: the entry's signal needs a target parameter this target
	// does not define, so it was never a question here.
	//
	// This is a fourth verdict rather than a shade of Unwatched because the
	// first version of this command collapsed them and immediately produced
	// the sentence "no reading resolved, so this window says nothing about
	// the entry" for an ingress entry on a plain service target. Nothing was
	// wrong and nothing needed looking into: an ingress entry is not silent
	// about that target, it is about something else. Reporting the two the
	// same way is the same conflation this project spends its four-state
	// model preventing, committed by the tool built to check for it.
	NotApplicable SilenceVerdict = "n/a"
)

// SilenceReport is one entry's behaviour on one target across the window.
type SilenceReport struct {
	Entry  string            `json:"entry"`
	Target map[string]string `json:"target"`

	// Reports counts incidents actually delivered. Any is a failure.
	Reports int `json:"reports"`
	// Peak is the highest value the signal reached, and Threshold is what it
	// was measured against. Relative says the threshold came from the learned
	// baseline, which makes Peak/Threshold a per-cluster fact rather than a
	// comparison against somebody else's number.
	Threshold float64 `json:"threshold"`
	Relative  bool    `json:"relative"`
	Peak      float64 `json:"peak"`
	HasPeak   bool    `json:"has_peak"`

	// LongestBreach is the longest unbroken run of ticks in breach, and Hold
	// is what the entry requires before reporting. The ratio between them is
	// the margin the entry is actually running on, which is not visible in a
	// threshold comparison: an entry can sit at 1.2x of its threshold all day
	// without firing and still be one slow afternoon from a false positive.
	LongestBreach time.Duration `json:"longest_breach"`
	Hold          time.Duration `json:"hold"`

	Samples int `json:"samples"`
	Blind   int `json:"blind"`
	// Skipped counts ticks where the entry did not apply to this target.
	Skipped int `json:"skipped"`
}

// Verdict classifies the report. Order matters: a delivered incident outranks
// everything, and never having been watched outranks having been quiet.
func (s SilenceReport) Verdict() SilenceVerdict {
	switch {
	case s.Reports > 0:
		return Fired
	case s.Samples == 0 && s.Skipped > 0:
		return NotApplicable
	case s.Samples == 0:
		return Unwatched
	case s.LongestBreach > 0:
		return Brushed
	default:
		return Silent
	}
}

// Margin is how many times over the longest healthy breach the hold duration
// is. Below 2 means an ordinary busy hour is inside the entry's tolerance.
// Infinity means the signal never breached at all.
func (s SilenceReport) Margin() float64 {
	if s.LongestBreach <= 0 {
		return math.Inf(1)
	}
	return float64(s.Hold) / float64(s.LongestBreach)
}

// Note explains the verdict in the words an operator needs, or is empty when
// the result speaks for itself.
func (s SilenceReport) Note() string {
	switch s.Verdict() {
	case Fired:
		return fmt.Sprintf("reported %d incident(s) on a cluster with no injected fault: this is a false positive", s.Reports)
	case Unwatched:
		return "no reading resolved, so this window says nothing about the entry"
	case NotApplicable:
		return "not a question for this target: the signal needs a parameter it does not define"
	case Brushed:
		m := s.Margin()
		if s.Hold == 0 {
			return fmt.Sprintf("breached for %s and has no hold duration at all: only luck kept it quiet", s.LongestBreach.Round(time.Second))
		}
		if m < 2 {
			return fmt.Sprintf("breached for %s against a %s hold, a margin of %.1fx: a busier hour fires this",
				s.LongestBreach.Round(time.Second), s.Hold.Round(time.Second), m)
		}
		return fmt.Sprintf("breached for %s against a %s hold, a margin of %.1fx",
			s.LongestBreach.Round(time.Second), s.Hold.Round(time.Second), m)
	}
	return ""
}

// Silence watches a healthy cluster for the given window and reports what each
// entry did. It injects nothing, so it is safe to point at a real mesh, and
// the preflight that guards the fault-injecting half is what should have run
// before it: a cluster that is already in breach is not a healthy one and this
// measures nothing there.
func (r *Runner) Silence(ctx context.Context, targets []detect.Target, window, warmup time.Duration) ([]SilenceReport, error) {
	// Keyed exactly the way the detector keys its own state, so a report and
	// the episode it describes cannot drift apart.
	acc := map[string]*SilenceReport{}
	runLen := map[string]time.Duration{}

	at := func(entry string, params map[string]string) *SilenceReport {
		k := targetKeyFor(entry, params)
		if acc[k] == nil {
			acc[k] = &SilenceReport{Entry: entry, Target: params}
		}
		return acc[k]
	}

	byID := map[string]catalog.Scenario{}
	for _, s := range r.scenarios {
		byID[s.ID] = s
	}

	d := detect.New(r.scenarios, targets, r.prom,
		func(_ context.Context, inc detect.Incident) error {
			at(inc.Scenario.ID, inc.Params).Reports++
			return nil
		})
	d.Log = func(format string, args ...any) { r.Log("detector: "+format, args...) }
	d.Now = r.Now
	d.Objects = r.Objects
	d.Triage = r.Triage
	d.Baseline = r.Baseline

	// Warm the baseline first, or the relative-threshold entries are measured
	// against a static fallback that production would never use. Silence
	// measured against the wrong threshold is not silence about anything: the
	// entry whose 200ms fallback this replaced peaks at 86ms idle and would
	// look serenely quiet either way, while saying nothing about the 181.9ms
	// it actually runs at.
	//
	// Learn from the floor rather than from whatever the cluster happened to
	// be doing, for the reason the fault half learned the hard way: a decaying
	// fault still under the static threshold teaches a normal several times
	// too high, and the entry then sleeps through the regression it exists to
	// catch.
	if warmup > 0 {
		if r.Baseline == nil {
			r.Baseline = baseline.New("", 0.05)
		}
		r.Log("warming the baseline for %s before the window opens, learning from the signal's floor", warmup)
		gate := &floorCollector{inner: r.Baseline, tolerance: 1.5}
		d.Baseline = gate
		until := r.Now().Add(warmup)
		for r.Now().Before(until) && ctx.Err() == nil {
			d.Tick(ctx, r.Now())
			r.Sleep(ctx, r.Poll)
		}
		gate.commit()
		d.Baseline = r.Baseline
		r.Log("baseline warmed: %d readings near the floor accepted, %d rejected as still-decaying",
			gate.accepted, gate.rejected)
	}

	start := r.Now()
	deadline := start.Add(window)
	lastTick := r.Wall()

	for r.Now().Before(deadline) && ctx.Err() == nil {
		// The same suspension guard the fault half uses. A window measured
		// across a closed laptop lid is not a window: Go's monotonic clock
		// stops, so a run frozen for hours reports minutes of silence it
		// never observed, and silence is exactly what this command sells.
		if gap := r.Wall().Sub(lastTick); gap > r.suspendThreshold() {
			return nil, fmt.Errorf("wall clock jumped %s between ticks: the process was suspended, so this window proves nothing",
				gap.Round(time.Second))
		}
		lastTick = r.Wall()

		cycle := d.Tick(ctx, r.Now())

		for _, ev := range cycle.Evaluations {
			rep := at(ev.Scenario, ev.Params)
			k := targetKeyFor(ev.Scenario, ev.Params)

			switch ev.Outcome {
			case detect.OutcomeFiring:
				rep.Samples++
				if !rep.HasPeak || ev.Value > rep.Peak {
					rep.Peak, rep.HasPeak = ev.Value, true
				}
				// One poll interval per consecutive in-breach tick. Measured
				// in time rather than ticks so the number is comparable to
				// the entry's hold duration, which is what it is for.
				runLen[k] += r.Poll
				if runLen[k] > rep.LongestBreach {
					rep.LongestBreach = runLen[k]
				}
			case detect.OutcomeClear:
				rep.Samples++
				if !rep.HasPeak || ev.Value > rep.Peak {
					rep.Peak, rep.HasPeak = ev.Value, true
				}
				runLen[k] = 0
			case detect.OutcomeNotApplicable:
				rep.Skipped++
			case detect.OutcomeBlind:
				rep.Blind++
				// A gap in sight breaks the run: two breaches either side of
				// blindness are not one long breach, and claiming otherwise
				// would overstate the problem.
				runLen[k] = 0
			default:
				runLen[k] = 0
			}
		}
		r.Sleep(ctx, r.Poll)
	}

	out := make([]SilenceReport, 0, len(acc))
	for _, rep := range acc {
		if s, ok := byID[rep.Entry]; ok {
			rep.Hold = detect.Hold(s)
			rep.Threshold, rep.Relative = d.EffectiveThreshold(s, detect.Target{Params: rep.Target})
		}
		out = append(out, *rep)
	}
	// Worst first, so the reason a run failed is the first line read.
	sort.Slice(out, func(i, j int) bool {
		rank := func(s SilenceReport) int {
			switch s.Verdict() {
			case Fired:
				return 0
			case Brushed:
				return 1
			case Unwatched:
				return 2
			case Silent:
				return 3
			default: // NotApplicable: nothing to look into at all
				return 4
			}
		}
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		if out[i].LongestBreach != out[j].LongestBreach {
			return out[i].LongestBreach > out[j].LongestBreach
		}
		return out[i].Entry < out[j].Entry
	})
	return out, nil
}

// DistinguishingParams returns, for each report, the target parameters that
// actually differ across the set.
//
// Labelling every row "payments.demo" would be worse than useless here: the
// two targets a proof directory watches share their service and namespace and
// differ by one parameter, so the obvious label prints the same string twice
// and the table reads as a duplicate-row bug. Showing only what differs is
// both shorter and the only part that carries information; when a run watches
// a single target it returns empty and the column disappears.
func DistinguishingParams(reports []SilenceReport) []string {
	values := map[string]map[string]bool{}
	for _, r := range reports {
		for k, v := range r.Target {
			if values[k] == nil {
				values[k] = map[string]bool{}
			}
			values[k][v] = true
		}
	}
	var out []string
	for k, vs := range values {
		// A key missing from some targets varies too, which is exactly the
		// case here: only one target defines ingress_workload.
		present := 0
		for _, r := range reports {
			if _, ok := r.Target[k]; ok {
				present++
			}
		}
		if len(vs) > 1 || present != len(reports) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Label renders a report's target using only the given keys.
func (s SilenceReport) Label(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, ok := s.Target[k]
		if !ok {
			v = "-"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}
