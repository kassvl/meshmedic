// Package detect evaluates catalog signals against live metrics and decides
// when a scenario fires. It owns the clock discipline: a threshold breach
// only becomes an incident after holding for the scenario's `for` duration,
// and a firing scenario stays quiet until it clears.
package detect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
	"github.com/kassvl/meshmedic/pkg/recorder"
)

// Querier is the slice of Prometheus the detector needs. prom.Client
// satisfies it; tests use scripted fakes. Signals go through Query, which
// enforces a single aggregated value; evidence goes through QuerySeries so
// labels survive into the report.
type Querier interface {
	Query(ctx context.Context, promql string) (float64, error)
	QuerySeries(ctx context.Context, promql string) ([]prom.Sample, error)
}

// Target is one set of template parameters watched against the catalog.
type Target struct {
	Params    map[string]string `yaml:"params"`
	Scenarios []string          `yaml:"scenarios"` // empty means every scenario
	// CoverageProbe overrides the control query that decides whether this
	// target is visible at all. Empty falls back to the detector's probe and
	// then to DefaultCoverageProbe.
	CoverageProbe string `yaml:"coverageProbe"`
}

func (t Target) wants(id string) bool {
	if len(t.Scenarios) == 0 {
		return true
	}
	for _, s := range t.Scenarios {
		if s == id {
			return true
		}
	}
	return false
}

// ObjectReader is the slice of the cluster the detector may read for
// configuration evidence. kube.Reader satisfies it; nil disables object
// evidence entirely, and metric detection works the same either way.
type ObjectReader interface {
	Get(ctx context.Context, apiVersion, kind, namespace, name string) (map[string]any, error)
}

// TriageReader is the slice of the cluster the detector may read for triage
// evidence: recent logs and recent rollouts. kube.Reader satisfies it; nil
// disables triage evidence.
type TriageReader interface {
	DeploymentNames(ctx context.Context, namespace string) ([]string, error)
	Logs(ctx context.Context, namespace, deployment string, sinceSeconds, tailLines int) (string, error)
	RecentRollouts(ctx context.Context, namespace string, within time.Duration) ([]kube.Rollout, error)
}

// BaselineStore is the slice of the baseline package the detector needs.
// baseline.Store satisfies it.
type BaselineStore interface {
	Observe(key string, value float64)
	Baseline(key string, minSamples int) (float64, bool)
}

// RecorderStore is the slice of the recorder package the detector needs.
// recorder.Recorder satisfies it.
type RecorderStore interface {
	Record(recorder.Fingerprint) error
}

// Incident is a scenario firing for a target.
type Incident struct {
	Scenario catalog.Scenario
	Params   map[string]string
	Value    float64
	// Threshold is the value the signal was actually compared against. For a
	// baseline-relative scenario this is the learned baseline times the
	// multiplier, not the static catalog threshold, so the report says what
	// really fired.
	Threshold        float64
	BaselineRelative bool
	Since            time.Time
	Evidence         []EvidenceResult
	ObjectEvidence   []ObjectEvidenceResult
	LogEvidence      []LogEvidenceResult
	RolloutEvidence  []RolloutEvidenceResult
}

// LogEvidenceResult is one log sweep's outcome: per-deployment matched lines.
type LogEvidenceResult struct {
	Name    string
	Matches map[string][]string // deployment -> matching log lines
	Err     error
}

// RolloutEvidenceResult is one rollout query's outcome.
type RolloutEvidenceResult struct {
	Name     string
	Rollouts []kube.Rollout
	Err      error
}

// EvidenceResult is one evidence query's outcome. A failed evidence query
// never blocks the incident; the error travels with it instead. Samples keep
// their labels: a per-workload breakdown is only evidence if the workload
// names survive.
type EvidenceResult struct {
	Name    string
	PromQL  string
	Samples []prom.Sample
	Err     error
}

// ObjectEvidenceResult is one object query's outcome. Like metric evidence,
// a failure never blocks the incident; the error travels with it.
type ObjectEvidenceResult struct {
	Name   string
	Ref    string            // Kind namespace/name, for the report
	Fields map[string]string // dotted path -> rendered value
	Err    error
}

// HandlerFunc receives each incident. Returning an error keeps the episode
// alive: the detector stays pending and delivers the incident again on the
// next tick, so a GitHub brownout cannot swallow a real incident. Return nil
// once the incident is durably handed off.
type HandlerFunc func(ctx context.Context, inc Incident) error

// Resolution is a firing incident returning to health: its signal fell back
// under the threshold. It closes the loop an Incident opened, carrying the
// interval the condition held so an operator reads MTTR without diffing two
// reports.
type Resolution struct {
	Scenario   catalog.Scenario
	Params     map[string]string
	Value      float64   // the recovered value, now under the threshold
	Threshold  float64   // the threshold the signal fell back under
	Since      time.Time // when the breach began
	ResolvedAt time.Time // when the signal returned under the threshold
}

// Duration is how long the signal was in breach before it recovered.
func (r Resolution) Duration() time.Duration { return r.ResolvedAt.Sub(r.Since) }

// ResolveFunc receives each resolution. Unlike HandlerFunc it is fire and
// forget: a resolution report is informational, so an error is logged and the
// state clears regardless. Nil disables resolution reporting entirely.
type ResolveFunc func(ctx context.Context, res Resolution) error

type state int

const (
	inactive state = iota // condition not met
	pending               // breached, waiting out the for-duration
	firing                // incident delivered, waiting for recovery
)

type entry struct {
	state state
	since time.Time
	// paramSkipLogged remembers that this scenario/target pair was reported
	// as not applicable (signal needs a param the target does not define),
	// so a whole-catalog watch logs the skip once instead of every tick.
	paramSkipLogged bool
	// applies is the rolling record of when this scenario last delivered an
	// incident for this target, which is what makes the catalog's
	// maxAppliesPerHour guardrail an actual limit rather than documentation.
	// It persists with the rest of the state, so restarting the process
	// cannot be used, deliberately or accidentally, to get around the limit.
	applies []time.Time
}

// withinLastHour returns how many applies fall inside the trailing hour from
// now, pruning the rest in place so the slice cannot grow without bound.
func (e *entry) withinLastHour(now time.Time) int {
	cutoff := now.Add(-time.Hour)
	kept := e.applies[:0]
	for _, at := range e.applies {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	e.applies = kept
	return len(kept)
}

// Detector evaluates targets against scenarios on every Tick.
type Detector struct {
	scenarios []catalog.Scenario
	targets   []Target
	querier   Querier
	handle    HandlerFunc
	states    map[string]*entry

	// Objects enables configuration evidence when set. Defaults to nil:
	// the CLI wires it when kubectl is available.
	Objects ObjectReader

	// Triage enables log and rollout evidence when set. Defaults to nil.
	Triage TriageReader

	// Baseline enables relative thresholds when set: signals with a
	// baselineMultiplier fire on a deviation from the target's learned
	// normal. Defaults to nil (static thresholds only).
	Baseline BaselineStore

	// Recorder, AnomalyWatch, AnomalyFactor and AnomalyMinSamples drive the
	// unmatched-incident recorder: generic signals baselined per target, and
	// a fingerprint written when one deviates by AnomalyFactor while no
	// catalog scenario is active for that target. Records only; needs
	// Baseline set too. AnomalyFactor defaults to 3, AnomalyMinSamples to 20.
	Recorder          RecorderStore
	AnomalyWatch      []catalog.Query
	AnomalyFactor     float64
	AnomalyMinSamples int

	// OnResolve, when set, is called once each time a firing incident recovers
	// (its signal falls back under the threshold). Nil disables resolution
	// reports; detection is identical either way.
	OnResolve ResolveFunc

	// CoverageProbe is the default control query proving a target is visible,
	// used for any target that does not set its own. Empty falls back to
	// DefaultCoverageProbe.
	CoverageProbe string

	// OnCycle, when set, receives the summary of every completed pass over
	// all targets. The CLI uses it to print the coverage line each cycle.
	OnCycle func(Cycle)

	// announced remembers the last effective threshold logged per key, so a
	// relative threshold is reported when it changes rather than every tick.
	announced map[string]float64

	// StateFile is where the incident lifecycle is persisted between ticks.
	// Empty disables persistence, which means a restart re-opens every
	// still-breaching incident.
	StateFile string

	// Now overrides the clock used for guardrail windows. Nil uses the real
	// clock; tests set it so a rolling hour does not take an hour.
	Now func() time.Time

	// Unlocked names scenarios whose catalog.lock hash is missing or does not
	// match, mapped to the reason. Such an entry does not run: what is on
	// disk is not what was reviewed and testbed-validated, and the difference
	// between accepted and merely committed is the whole point of the lock.
	// It is reported every cycle rather than dropped, so an operator learns
	// which part of the catalog is not covering them.
	Unlocked map[string]string

	// Log receives non-fatal evaluation problems (template errors, query
	// failures). Defaults to discarding them; the CLI wires it to stderr.
	Log func(format string, args ...any)
}

func New(scenarios []catalog.Scenario, targets []Target, q Querier, h HandlerFunc) *Detector {
	return &Detector{
		scenarios: scenarios,
		targets:   targets,
		querier:   q,
		handle:    h,
		states:    map[string]*entry{},
		Log:       func(string, ...any) {},
	}
}

// Run ticks immediately and then on every interval until ctx is done.
func (d *Detector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		d.Tick(ctx, time.Now())
		// Persist the learned baseline after each tick so it survives a
		// restart. Best effort: a save failure logs and the loop continues.
		if p, ok := d.Baseline.(interface{ Save() error }); ok {
			if err := p.Save(); err != nil {
				d.Log("baseline: save: %v", err)
			}
		}
		// Persist the incident lifecycle too, for the same reason and a
		// sharper one: without it a restart re-opens every still-breaching
		// incident, and in GitOps mode that is a second pull request for an
		// incident already under review.
		if err := d.SaveState(d.StateFile); err != nil {
			d.Log("state: save: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick evaluates every watched target/scenario pair once. The caller owns
// the clock, which keeps the state machine testable without sleeping.
//
// Evaluation is two-pass per target: first every signal advances its state
// machine, then cascade suppression is decided across the whole target, and
// only unsuppressed incidents are delivered. A suppressed scenario stays
// pending, so it still fires later if its suppressor clears first.
func (d *Detector) Tick(ctx context.Context, now time.Time) Cycle {
	cycle := Cycle{Started: now}
	for _, t := range d.targets {
		type dueIncident struct {
			key   string
			s     catalog.Scenario
			value float64
		}
		var due []dueIncident
		inBreach := map[string]bool{}

		// Coverage probe first. It answers the only question that makes the
		// rest of the cycle meaningful: can this tool see this target at all?
		// A target that fails it is unobserved, and every scenario against it
		// reports blind rather than clear, because silence from a blind
		// detector is not a finding.
		observed, probeReason := d.probeCoverage(ctx, t)
		if observed {
			cycle.Observed++
		} else {
			cycle.Unobserved++
			cycle.UnobservedTargets = append(cycle.UnobservedTargets, t.Params)
			d.Log("target %s is unobserved: %s", formatParams(t.Params), probeReason)
		}

		for _, s := range d.scenarios {
			if !t.wants(s.ID) {
				continue
			}
			key := targetKey(s.ID, t.Params)

			if reason, unlocked := d.Unlocked[s.ID]; unlocked {
				cycle.record(Evaluation{Scenario: s.ID, Params: t.Params,
					Outcome: OutcomeUnlocked, Reason: reason})
				continue
			}

			if !observed {
				// The scenario cannot be evaluated honestly, and this
				// includes the absence-is-signal entries. Their flag changes
				// what an empty *scenario* result means once the target is
				// known to be visible; it does not license firing an outage
				// out of our own blindness. Distinguishing "the traffic
				// stopped" from "we cannot see this target" is precisely the
				// coverage probe's job, and it just answered the latter.
				//
				// Clear any pending progress too, so a breach measured while
				// blind cannot mature into an incident once sight returns.
				if st := d.states[key]; st != nil && st.state == pending {
					st.state, st.since = inactive, time.Time{}
				}
				cycle.record(Evaluation{Scenario: s.ID, Params: t.Params,
					Outcome: OutcomeBlind, Reason: "target unobserved: " + probeReason})
				continue
			}

			ev, isDue := d.evaluateSignal(ctx, now, key, t, s)
			cycle.record(ev)
			if isDue {
				due = append(due, dueIncident{key, s, ev.Value})
			} else if st := d.states[key]; st != nil {
				// Not due, but the state says whether that is because the
				// hold has not elapsed or because this episode was already
				// reported on an earlier tick. Both look like silence.
				switch st.state {
				case pending:
					cycle.Waiting++
				case firing:
					cycle.Reported++
				}
			}
			if d.states[key].state != inactive {
				inBreach[s.ID] = true
			}
		}

		suppressedBy := map[string]string{}
		for _, s := range d.scenarios {
			if !inBreach[s.ID] {
				continue
			}
			for _, id := range s.Suppresses {
				suppressedBy[id] = s.ID
			}
		}

		// Unmatched-incident recorder: if the catalog has nothing to say about
		// this target right now, watch the generic anomaly signals and record
		// any that deviate from their learned normal. Records only.
		if d.Recorder != nil && d.Baseline != nil && len(d.AnomalyWatch) > 0 {
			catalogActive := len(inBreach) > 0
			for _, w := range d.AnomalyWatch {
				d.watchAnomaly(ctx, t, w, catalogActive)
			}
		}

		for _, du := range due {
			if by, ok := suppressedBy[du.s.ID]; ok {
				d.Log("%s: suppressed by %s: cascade symptom, not a second incident", du.key, by)
				cycle.Suppressed++
				continue
			}
			switch d.deliver(ctx, du.key, du.s, t, du.value) {
			case deliveredIncident:
				cycle.Reported++
			case deliveryRateLimited:
				cycle.RateLimited++
			case deliveryRetrying:
				cycle.Retrying++
			}
		}
	}
	if d.OnCycle != nil {
		d.OnCycle(cycle)
	}
	return cycle
}

// record files one evaluation into the cycle summary and counts its outcome.
func (c *Cycle) record(ev Evaluation) {
	c.Evaluations = append(c.Evaluations, ev)
	switch ev.Outcome {
	case OutcomeFiring:
		c.InBreach++
	case OutcomeClear:
		c.Clear++
	case OutcomeBlind:
		c.Blind++
	case OutcomeUnlocked:
		c.Unlocked++
	}
}

// probeCoverage asks whether this target resolves to any series in base mesh
// telemetry. It is deliberately a different query from any scenario signal:
// a scenario returning nothing is ambiguous (the failure may be exactly that
// traffic stopped), while the probe returning nothing means the tool cannot
// see the target at all. That distinction is what lets an absence-based
// entry like traffic-vanished-triage keep working without the detector ever
// having to guess which kind of silence it is looking at.
func (d *Detector) probeCoverage(ctx context.Context, t Target) (bool, string) {
	tmpl := t.CoverageProbe
	if tmpl == "" {
		tmpl = d.CoverageProbe
	}
	if tmpl == "" {
		tmpl = DefaultCoverageProbe
	}
	query, err := renderQuery("coverage-probe", tmpl, t.Params)
	if err != nil {
		// A probe that cannot be rendered cannot prove anything. Saying so is
		// the honest outcome; setting coverageProbe on the target fixes it.
		return false, fmt.Sprintf("coverage probe does not render for this target: %v", err)
	}
	samples, err := d.querier.QuerySeries(ctx, query)
	switch {
	case errors.Is(err, prom.ErrNoData):
		return false, "coverage probe returned no series: no mesh telemetry for this target"
	case err != nil:
		return false, fmt.Sprintf("coverage probe failed: %v", err)
	case len(samples) == 0:
		return false, "coverage probe returned an empty result"
	}
	return true, ""
}

// evaluateSignal advances one scenario's state machine and reports whether
// its incident is due this tick. Delivery happens in Tick, after cascade
// suppression is decided across the target.
func (d *Detector) evaluateSignal(ctx context.Context, now time.Time, key string, t Target, s catalog.Scenario) (Evaluation, bool) {
	ev := Evaluation{Scenario: s.ID, Params: t.Params}
	st := d.states[key]
	if st == nil {
		st = &entry{}
		d.states[key] = st
	}

	query, err := renderQuery(s.ID, s.Signal.PromQL, t.Params)
	if err != nil {
		// A target that lacks a param the signal template needs is not an
		// error: the scenario does not apply to that target (an ingress
		// entry evaluated for a plain service target, say). Report it once
		// and stay quiet afterwards. It is not counted as an outcome at all,
		// because a scenario that does not apply was never a question.
		if strings.Contains(err.Error(), "map has no entry for key") {
			if !st.paramSkipLogged {
				d.Log("%s: not applicable to this target, signal needs a param the target does not define: %v", key, err)
				st.paramSkipLogged = true
			}
			ev.Outcome = OutcomeNotApplicable
			return ev, false
		}
		d.Log("%s: rendering signal: %v", key, err)
		ev.Outcome, ev.Reason = OutcomeBlind, "signal template does not render: "+err.Error()
		return ev, false
	}
	value, err := d.querier.Query(ctx, query)
	switch {
	case errors.Is(err, prom.ErrNoData):
		// An empty result and a zero value are different facts and stay
		// different all the way to the report. For an ordinary entry an
		// empty result means the signal could not be measured: blind, not
		// clear. Any pending progress is cleared, because a breach we
		// stopped being able to see must not mature into an incident.
		//
		// For an entry that declares absence is its signal, an empty result
		// is a legitimate zero and flows on into the threshold comparison.
		// The coverage probe has already established that the target is
		// visible, so this really is "the traffic stopped", not "we went
		// blind".
		if !s.Signal.AbsenceIsSignal {
			// An empty result on a target the coverage probe just proved
			// visible is a real answer, not an admission. A counter-based
			// failure signal (response_code="403", response_flags="UO")
			// returns nothing precisely when the failure is not happening,
			// and on a healthy mesh that is most entries most of the time:
			// measured live, 41 of 49 evaluations. Calling those blind would
			// bury the handful that genuinely are, which is the failure the
			// coverage number exists to prevent.
			//
			// The reason is still recorded, so "clear because nothing
			// matched" stays distinguishable from "clear because the value
			// was under threshold", and an entry that is empty forever is
			// caught at config time by validate --against-prometheus rather
			// than re-derived every cycle.
			st.state, st.since = inactive, time.Time{}
			ev.Outcome = OutcomeClear
			ev.Reason = "signal returned no series; the target is observed, so nothing matched"
			return ev, false
		}
		value = 0
	case err != nil:
		// A transient scrape failure must not reset a pending breach, so the
		// state survives the error untouched. It is still blind: we did not
		// get an answer this cycle and must not report one.
		d.Log("%s: query: %v", key, err)
		ev.Outcome, ev.Reason = OutcomeBlind, "signal query failed: "+err.Error()
		return ev, false
	}
	ev.Value = value

	threshold := d.effectiveThreshold(s, t)
	if !breached(value, s.Signal.Comparison, threshold) {
		// A firing incident whose signal falls back under the threshold has
		// recovered: close the loop with a resolution before clearing state.
		// Only the firing->clear edge resolves, so a pending breach that never
		// fired, and an already-clear signal, produce nothing.
		if st.state == firing && d.OnResolve != nil {
			d.resolve(ctx, s, t, value, threshold, st.since, now)
		}
		st.state, st.since = inactive, time.Time{}
		// Only healthy (non-breaching) values feed the baseline, so an
		// ongoing incident can never drift the learned normal upward and
		// silence itself.
		if d.Baseline != nil && s.Signal.BaselineMultiplier > 0 {
			d.Baseline.Observe(targetKey(s.ID, t.Params), value)
		}
		ev.Outcome = OutcomeClear
		return ev, false
	}

	// The signal is in breach. Whether it is an incident yet depends on the
	// for-duration; either way the honest outcome is firing, because the
	// threshold comparison returned a real answer.
	ev.Outcome = OutcomeFiring
	switch st.state {
	case inactive:
		st.state, st.since = pending, now
		// A zero for-duration is due on this same tick, so fall through
		// by re-checking immediately.
		fallthrough
	case pending:
		if now.Sub(st.since) >= forDuration(s) {
			return ev, true
		}
	case firing:
		// Already delivered; stay quiet until the condition clears.
	}
	return ev, false
}

// watchAnomaly evaluates one generic anomaly signal for a target. It records
// a fingerprint when the signal deviates from its learned baseline by the
// configured factor while no catalog scenario is active, and feeds only
// non-deviating values into the baseline so an anomaly cannot become the new
// normal. It never fires an incident or a patch; recording is the whole job.
func (d *Detector) watchAnomaly(ctx context.Context, t Target, w catalog.Query, catalogActive bool) {
	query, err := renderQuery("anomaly/"+w.Name, w.PromQL, t.Params)
	if err != nil {
		d.Log("anomaly %s: rendering: %v", w.Name, err)
		return
	}
	value, err := d.querier.Query(ctx, query)
	if err != nil {
		// No data or a scrape blip: nothing to record or learn this tick.
		return
	}
	factor := d.AnomalyFactor
	if factor <= 1 {
		factor = 3
	}
	minSamples := d.AnomalyMinSamples
	if minSamples <= 0 {
		minSamples = 20
	}
	key := targetKey("anomaly-"+w.Name, t.Params)
	base, ready := d.Baseline.Baseline(key, minSamples)
	deviating := ready && base > 0 && (value > base*factor || value < base/factor)

	if deviating && !catalogActive {
		if err := d.Recorder.Record(recorder.Fingerprint{
			Target:   t.Params,
			Signal:   w.Name,
			Value:    value,
			Baseline: base,
			Factor:   value / base,
		}); err != nil {
			d.Log("anomaly %s: record: %v", w.Name, err)
		}
	}
	if !deviating {
		d.Baseline.Observe(key, value)
	}
}

// effectiveThreshold returns the threshold to compare against: the signal's
// learned baseline times its multiplier once the baseline is trusted,
// otherwise the static threshold. A scenario with no baselineMultiplier, or
// one whose baseline has not warmed up, always uses the static threshold.
func (d *Detector) effectiveThreshold(s catalog.Scenario, t Target) float64 {
	if d.Baseline == nil || s.Signal.BaselineMultiplier <= 0 {
		return s.Signal.Threshold
	}
	minSamples := s.Signal.BaselineMinSamples
	if minSamples <= 0 {
		minSamples = 20
	}
	if base, ready := d.Baseline.Baseline(targetKey(s.ID, t.Params), minSamples); ready {
		effective := base * s.Signal.BaselineMultiplier
		// Say what threshold is actually in force, once per change. A
		// relative threshold is invisible otherwise: an operator watching a
		// quiet entry cannot tell a well-calibrated one from an entry whose
		// learned normal drifted so high it can no longer fire. Diagnosing
		// exactly that on the testbed cost two full proof cycles, because the
		// number existed only inside this function.
		key := targetKey(s.ID, t.Params)
		if d.announced == nil {
			d.announced = map[string]float64{}
		}
		if prev, seen := d.announced[key]; !seen || math.Abs(prev-effective) > prev*0.1 {
			d.announced[key] = effective
			d.Log("%s: learned normal %.4g, effective threshold %.4g (%gx), static fallback %.4g",
				s.ID, base, effective, s.Signal.BaselineMultiplier, s.Signal.Threshold)
		}
		return effective
	}
	return s.Signal.Threshold
}

// targetKey is a stable per-target-per-scenario key: scenario id plus the
// target params in sorted order. Stability is what lets both the learned
// baseline and the incident lifecycle survive a restart and a reordering of
// the config file; an index-based key silently reassigns every open incident
// to a different target the moment someone edits the target list.
func targetKey(scenarioID string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(scenarioID)
	for _, k := range keys {
		b.WriteString("|")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(params[k])
	}
	return b.String()
}

// delivery is what became of a due incident. Three of deliver's exits are
// silent from the outside and only one of them produced a document, so the
// cycle summary has to be told which.
type delivery int

const (
	deliveredIncident   delivery = iota // the handler took it
	deliveryRateLimited                 // maxAppliesPerHour stopped a further proposal
	deliveryRetrying                    // the handler errored; the episode is kept
)

func (d *Detector) deliver(ctx context.Context, key string, s catalog.Scenario, t Target, value float64) delivery {
	st := d.states[key]

	// The catalog's maxAppliesPerHour guardrail, enforced. Every entry
	// declares one and, until now, nothing read it: the safety story
	// promised a rate limit the code did not implement. A limit of zero or
	// less means unlimited, which is what an entry that omits the field is
	// asking for.
	//
	// Hitting the limit does not silence the incident, because a guardrail
	// that hides what it blocked is its own fail-open. It stops the handler
	// from proposing another change and says so loudly; the operator still
	// learns the signal is breaching, and learns that MeshMedic has stopped
	// proposing fixes for it.
	if limit := s.Guardrails.MaxAppliesPerHour; limit > 0 {
		if n := st.withinLastHour(d.now()); n >= limit {
			d.Log("%s: guardrail hit, %d of %d applies used in the last hour; the signal is still breaching at %.4g but no further change will be proposed until the window clears",
				key, n, limit, value)
			st.state = firing
			return deliveryRateLimited
		}
	}

	threshold := d.effectiveThreshold(s, t)
	relative := d.Baseline != nil && s.Signal.BaselineMultiplier > 0 && threshold != s.Signal.Threshold
	err := d.handle(ctx, Incident{
		Scenario:         s,
		Params:           t.Params,
		Value:            value,
		Threshold:        threshold,
		BaselineRelative: relative,
		Since:            st.since,
		Evidence:         d.gatherEvidence(ctx, s, t.Params),
		ObjectEvidence:   d.gatherObjectEvidence(ctx, s, t.Params),
		LogEvidence:      d.gatherLogEvidence(ctx, s, t.Params),
		RolloutEvidence:  d.gatherRolloutEvidence(ctx, s, t.Params),
	})
	if err != nil {
		d.Log("%s: handler failed, keeping the episode for retry: %v", key, err)
		return deliveryRetrying
	}
	// Count the apply only once the handler has taken it. A pull request that
	// never opened must not consume the entry's hourly budget.
	st.applies = append(st.applies, d.now())
	st.state = firing
	return deliveredIncident
}

// now is the detector's clock, injectable so the guardrail's rolling hour is
// testable without sleeping. Nil means the real clock.
func (d *Detector) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// resolve reports a firing incident's recovery. Best effort: a failing
// resolution handler is logged, never retried, and never blocks clearing the
// state, because the incident is genuinely over.
func (d *Detector) resolve(ctx context.Context, s catalog.Scenario, t Target, value, threshold float64, since, now time.Time) {
	if err := d.OnResolve(ctx, Resolution{
		Scenario:   s,
		Params:     t.Params,
		Value:      value,
		Threshold:  threshold,
		Since:      since,
		ResolvedAt: now,
	}); err != nil {
		d.Log("%s: resolution handler: %v", s.ID, err)
	}
}

func (d *Detector) gatherEvidence(ctx context.Context, s catalog.Scenario, params map[string]string) []EvidenceResult {
	var results []EvidenceResult
	for _, q := range s.Evidence {
		r := EvidenceResult{Name: q.Name}
		r.PromQL, r.Err = renderQuery(s.ID+"/"+q.Name, q.PromQL, params)
		if r.Err == nil {
			r.Samples, r.Err = d.querier.QuerySeries(ctx, r.PromQL)
		}
		results = append(results, r)
	}
	return results
}

func (d *Detector) gatherObjectEvidence(ctx context.Context, s catalog.Scenario, params map[string]string) []ObjectEvidenceResult {
	if d.Objects == nil || len(s.ObjectEvidence) == 0 {
		return nil
	}
	results := make([]ObjectEvidenceResult, 0, len(s.ObjectEvidence))
	for _, q := range s.ObjectEvidence {
		results = append(results, d.objectEvidence(ctx, s, q, params))
	}
	return results
}

func (d *Detector) objectEvidence(ctx context.Context, s catalog.Scenario, q catalog.ObjectQuery, params map[string]string) ObjectEvidenceResult {
	r := ObjectEvidenceResult{Name: q.Name}
	name, err := renderQuery(s.ID+"/"+q.Name+"/object", q.Object, params)
	if err != nil {
		r.Err = err
		return r
	}
	ns, err := renderQuery(s.ID+"/"+q.Name+"/namespace", q.Namespace, params)
	if err != nil {
		r.Err = err
		return r
	}
	r.Ref = q.Kind + " " + ns + "/" + name
	obj, err := d.Objects.Get(ctx, q.APIVersion, q.Kind, ns, name)
	if err != nil {
		r.Err = err
		return r
	}
	r.Fields = map[string]string{}
	for _, path := range q.Fields {
		val, err := kube.ExtractField(obj, path)
		if err != nil {
			val = fmt.Sprintf("unavailable (%v)", err)
		}
		r.Fields[path] = val
	}
	return r
}

func (d *Detector) gatherLogEvidence(ctx context.Context, s catalog.Scenario, params map[string]string) []LogEvidenceResult {
	if d.Triage == nil || len(s.LogEvidence) == 0 {
		return nil
	}
	var results []LogEvidenceResult
	for _, q := range s.LogEvidence {
		r := LogEvidenceResult{Name: q.Name}
		ns, err := renderQuery(s.ID+"/"+q.Name+"/namespace", q.Namespace, params)
		if err != nil {
			r.Err = err
			results = append(results, r)
			continue
		}
		// Patterns were validated at catalog load, so compilation here
		// cannot fail; ORing them keeps one pass per log line.
		re := regexp.MustCompile("(?i)" + strings.Join(q.Patterns, "|"))
		deployments, err := d.Triage.DeploymentNames(ctx, ns)
		if err != nil {
			r.Err = err
			results = append(results, r)
			continue
		}
		since, maxLines := q.SinceSeconds, q.MaxLines
		if since <= 0 {
			since = 300
		}
		if maxLines <= 0 {
			maxLines = 10
		}
		r.Matches = map[string][]string{}
		for _, dep := range deployments {
			logs, err := d.Triage.Logs(ctx, ns, dep, since, 200)
			if err != nil {
				d.Log("%s/%s: logs for %s: %v", s.ID, q.Name, dep, err)
				continue
			}
			for _, line := range strings.Split(logs, "\n") {
				if len(r.Matches[dep]) >= maxLines {
					break
				}
				if re.MatchString(line) {
					r.Matches[dep] = append(r.Matches[dep], strings.TrimSpace(line))
				}
			}
		}
		results = append(results, r)
	}
	return results
}

func (d *Detector) gatherRolloutEvidence(ctx context.Context, s catalog.Scenario, params map[string]string) []RolloutEvidenceResult {
	if d.Triage == nil || len(s.RolloutEvidence) == 0 {
		return nil
	}
	var results []RolloutEvidenceResult
	for _, q := range s.RolloutEvidence {
		r := RolloutEvidenceResult{Name: q.Name}
		ns, err := renderQuery(s.ID+"/"+q.Name+"/namespace", q.Namespace, params)
		if err == nil {
			r.Rollouts, err = d.Triage.RecentRollouts(ctx, ns, time.Duration(q.WithinMinutes)*time.Minute)
		}
		r.Err = err
		results = append(results, r)
	}
	return results
}

func breached(v float64, op string, threshold float64) bool {
	switch op {
	case ">":
		return v > threshold
	case "<":
		return v < threshold
	case ">=":
		return v >= threshold
	case "<=":
		return v <= threshold
	}
	return false
}

func forDuration(s catalog.Scenario) time.Duration {
	if s.Signal.For == "" {
		return 0
	}
	// Parse errors are impossible here: catalog validation rejects them.
	dur, _ := time.ParseDuration(s.Signal.For)
	return dur
}

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

// formatParams renders a target's params for a log line, sorted so the same
// target always reads the same way.
func formatParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, " ")
}
