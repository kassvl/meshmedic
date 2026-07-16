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
	"text/template"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/prom"
)

// Querier is the slice of Prometheus the detector needs. prom.Client
// satisfies it; tests use scripted fakes.
type Querier interface {
	Query(ctx context.Context, promql string) (float64, error)
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

// Incident is a scenario firing for a target.
type Incident struct {
	Scenario catalog.Scenario
	Params   map[string]string
	Value    float64
	Since    time.Time
	Evidence []EvidenceResult
}

// EvidenceResult is one evidence query's outcome. A failed evidence query
// never blocks the incident; the error travels with it instead.
type EvidenceResult struct {
	Name   string
	PromQL string
	Value  float64
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
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick evaluates every watched target/scenario pair once. The caller owns
// the clock, which keeps the state machine testable without sleeping.
func (d *Detector) Tick(ctx context.Context, now time.Time) {
	for ti, t := range d.targets {
		for _, s := range d.scenarios {
			if !t.wants(s.ID) {
				continue
			}
			d.evaluate(ctx, now, ti, t, s)
		}
	}
}

func (d *Detector) evaluate(ctx context.Context, now time.Time, ti int, t Target, s catalog.Scenario) {
	key := fmt.Sprintf("%d/%s", ti, s.ID)
	st := d.states[key]
	if st == nil {
		st = &entry{}
		d.states[key] = st
	}

	query, err := renderQuery(s.ID, s.Signal.PromQL, t.Params)
	if err != nil {
		d.Log("%s: rendering signal: %v", key, err)
		return
	}
	value, err := d.querier.Query(ctx, query)
	switch {
	case errors.Is(err, prom.ErrNoData):
		// No traffic is not an incident; clear any progress.
		st.state, st.since = inactive, time.Time{}
		return
	case err != nil:
		// A transient scrape failure must not reset a pending breach,
		// so the state survives the error untouched.
		d.Log("%s: query: %v", key, err)
		return
	}

	if !breached(value, s.Signal.Comparison, s.Signal.Threshold) {
		st.state, st.since = inactive, time.Time{}
		return
	}

	switch st.state {
	case inactive:
		st.state, st.since = pending, now
		// A zero for-duration fires on the next evaluation below, so
		// fall through by re-checking immediately.
		fallthrough
	case pending:
		if now.Sub(st.since) >= forDuration(s) {
			err := d.handle(ctx, Incident{
				Scenario: s,
				Params:   t.Params,
				Value:    value,
				Since:    st.since,
				Evidence: d.gatherEvidence(ctx, s, t.Params),
			})
			if err != nil {
				d.Log("%s: handler failed, keeping the episode for retry: %v", key, err)
				return
			}
			st.state = firing
		}
	case firing:
		// Already delivered; stay quiet until the condition clears.
	}
}

func (d *Detector) gatherEvidence(ctx context.Context, s catalog.Scenario, params map[string]string) []EvidenceResult {
	var results []EvidenceResult
	for _, q := range s.Evidence {
		r := EvidenceResult{Name: q.Name}
		r.PromQL, r.Err = renderQuery(s.ID+"/"+q.Name, q.PromQL, params)
		if r.Err == nil {
			r.Value, r.Err = d.querier.Query(ctx, r.PromQL)
		}
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
