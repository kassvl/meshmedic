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
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
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

type state int

const (
	inactive state = iota // condition not met
	pending               // breached, waiting out the for-duration
	firing                // incident delivered, waiting for recovery
)

type entry struct {
	state state
	since time.Time
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
func (d *Detector) Tick(ctx context.Context, now time.Time) {
	for ti, t := range d.targets {
		type dueIncident struct {
			key   string
			s     catalog.Scenario
			value float64
		}
		var due []dueIncident
		inBreach := map[string]bool{}
		for _, s := range d.scenarios {
			if !t.wants(s.ID) {
				continue
			}
			key := fmt.Sprintf("%d/%s", ti, s.ID)
			value, isDue := d.evaluateSignal(ctx, now, key, t, s)
			if isDue {
				due = append(due, dueIncident{key, s, value})
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

		for _, du := range due {
			if by, ok := suppressedBy[du.s.ID]; ok {
				d.Log("%s: suppressed by %s: cascade symptom, not a second incident", du.key, by)
				continue
			}
			d.deliver(ctx, du.key, du.s, t, du.value)
		}
	}
}

// evaluateSignal advances one scenario's state machine and reports whether
// its incident is due this tick. Delivery happens in Tick, after cascade
// suppression is decided across the target.
func (d *Detector) evaluateSignal(ctx context.Context, now time.Time, key string, t Target, s catalog.Scenario) (float64, bool) {
	st := d.states[key]
	if st == nil {
		st = &entry{}
		d.states[key] = st
	}

	query, err := renderQuery(s.ID, s.Signal.PromQL, t.Params)
	if err != nil {
		d.Log("%s: rendering signal: %v", key, err)
		return 0, false
	}
	value, err := d.querier.Query(ctx, query)
	switch {
	case errors.Is(err, prom.ErrNoData):
		// No traffic is not an incident; clear any progress.
		st.state, st.since = inactive, time.Time{}
		return 0, false
	case err != nil:
		// A transient scrape failure must not reset a pending breach,
		// so the state survives the error untouched.
		d.Log("%s: query: %v", key, err)
		return 0, false
	}

	threshold := d.effectiveThreshold(s, t)
	if !breached(value, s.Signal.Comparison, threshold) {
		st.state, st.since = inactive, time.Time{}
		// Only healthy (non-breaching) values feed the baseline, so an
		// ongoing incident can never drift the learned normal upward and
		// silence itself.
		if d.Baseline != nil && s.Signal.BaselineMultiplier > 0 {
			d.Baseline.Observe(baselineKey(s.ID, t.Params), value)
		}
		return value, false
	}

	switch st.state {
	case inactive:
		st.state, st.since = pending, now
		// A zero for-duration is due on this same tick, so fall through
		// by re-checking immediately.
		fallthrough
	case pending:
		if now.Sub(st.since) >= forDuration(s) {
			return value, true
		}
	case firing:
		// Already delivered; stay quiet until the condition clears.
	}
	return value, false
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
	if base, ready := d.Baseline.Baseline(baselineKey(s.ID, t.Params), minSamples); ready {
		return base * s.Signal.BaselineMultiplier
	}
	return s.Signal.Threshold
}

// baselineKey is a stable per-target-per-scenario key: scenario id plus the
// target params in sorted order, so a persisted baseline survives a restart
// and a config reorder.
func baselineKey(scenarioID string, params map[string]string) string {
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

func (d *Detector) deliver(ctx context.Context, key string, s catalog.Scenario, t Target, value float64) {
	st := d.states[key]
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
		return
	}
	st.state = firing
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
