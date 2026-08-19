// Package calibrate answers one question about a catalog entry that no other
// check in this repository asks: on a cluster where nothing is wrong, does
// this entry stay quiet, and by how much?
//
// Silence alone is a weak answer. An entry whose healthy peak sits at 199
// against a threshold of 200 is silent today and pages at 3am tomorrow, and a
// run that only records "nothing fired" calls it passing. So this measures
// headroom: how far the healthy extreme sits from the threshold, as a factor.
// An entry with 80x headroom is calibrated. An entry with 1.1x is a future
// incident with a date on it.
//
// Three design decisions carry most of the value, and each of them is a way
// this check could have been wrong:
//
//   - It tracks the extreme, not the average. A signal averaging 150ms that
//     peaks at 250ms will fire, and its average says it will not.
//
//   - It measures the longest consecutive breach against the entry's own
//     `for` duration. A single 10-second excursion over the threshold does not
//     fire an entry that requires 90 seconds, so calling it miscalibrated
//     would be a false positive. It is still worth reporting, as marginal:
//     the entry came within reach of firing on a healthy cluster.
//
//   - It refuses to grade an entry that produced no data. An entry whose
//     metric does not exist is perfectly silent, and treating that as
//     calibration would award the best possible score to a dead entry. That
//     is the single most dangerous confusion available here, and it is why
//     `unmeasured` is a verdict rather than a footnote.
//
// The gate never judges an entry on samples that did not resolve. Every
// verdict below is computed from observations that returned a value.
package calibrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/prom"
)

// Verdict is one entry's standing on a healthy cluster.
type Verdict string

const (
	// Calibrated: the entry stayed quiet with room to spare.
	Calibrated Verdict = "calibrated"
	// Marginal: the entry stayed quiet, but its healthy extreme came within
	// the configured factor of the threshold, or crossed it briefly without
	// holding long enough to fire. It will fire on an ordinary busy day.
	Marginal Verdict = "marginal"
	// Miscalibrated: on a cluster with nothing wrong, the signal held past
	// the threshold for the entry's full `for` duration. This entry fires on
	// health, and in GitOps mode that is a pull request for a non-problem.
	Miscalibrated Verdict = "miscalibrated"
	// Unmeasured: no sample resolved, so the entry's silence proves nothing
	// about its calibration. Not a pass.
	Unmeasured Verdict = "unmeasured"
	// NotApplicable: the entry's signal needs a template parameter this
	// target does not define, so it was never a question for this target.
	NotApplicable Verdict = "not-applicable"
)

// Observation is one (entry, target) pair measured over the window.
type Observation struct {
	Scenario string
	Params   map[string]string

	Samples  int // observations that returned a value
	Empty    int // observations that returned no series
	Errors   int // observations that failed
	Min, Max float64

	Threshold  float64
	Comparison string
	For        time.Duration
	// LongestBreach is the longest consecutive interval the signal spent past
	// the threshold. Compared against For, this is what separates "would have
	// fired" from "flickered".
	LongestBreach time.Duration

	// Headroom is how many times the healthy extreme must change to reach the
	// threshold. Above 1 means quiet; the higher the safer. Infinity when the
	// extreme is zero and the threshold is not.
	Headroom float64
	Verdict  Verdict
	Note     string
}

// Config controls how strict the gate is.
type Config struct {
	// Window is how long to observe. Must comfortably exceed the longest
	// `for` duration in the catalog, or an entry cannot be shown to hold.
	Window time.Duration
	// Interval between samples.
	Interval time.Duration
	// MarginFactor is the headroom below which a quiet entry is still called
	// marginal. 2 means "an entry whose healthy peak is within half its
	// threshold is too close for comfort".
	MarginFactor float64
	// MinSamples is how many resolved observations an entry needs before a
	// verdict other than Unmeasured is issued. One lucky sample is not
	// evidence of calibration.
	MinSamples int
}

// Defaults fills in anything the caller left at zero.
func (c Config) Defaults() Config {
	if c.Window <= 0 {
		c.Window = 10 * time.Minute
	}
	if c.Interval <= 0 {
		c.Interval = 15 * time.Second
	}
	if c.MarginFactor <= 1 {
		c.MarginFactor = 2
	}
	if c.MinSamples <= 0 {
		c.MinSamples = 5
	}
	return c
}

// Querier is the slice of Prometheus the gate needs.
type Querier interface {
	Query(ctx context.Context, promql string) (float64, error)
}

// Target is one set of template parameters to calibrate against.
type Target struct {
	Params map[string]string
}

// Runner accumulates observations across a window.
type Runner struct {
	cfg       Config
	scenarios []catalog.Scenario
	targets   []Target
	querier   Querier

	// obs is keyed by scenario id + target, mirroring the detector's own
	// stable keying so a report lines up with what the detector would do.
	obs map[string]*Observation
	// breachStart remembers when the current consecutive breach began, so the
	// longest one can be measured without storing every sample.
	breachStart map[string]time.Time

	// Log receives per-sample problems. Defaults to discarding them.
	Log func(format string, args ...any)
}

// New builds a runner.
func New(cfg Config, scenarios []catalog.Scenario, targets []Target, q Querier) *Runner {
	return &Runner{
		cfg:         cfg.Defaults(),
		scenarios:   scenarios,
		targets:     targets,
		querier:     q,
		obs:         map[string]*Observation{},
		breachStart: map[string]time.Time{},
		Log:         func(string, ...any) {},
	}
}

// Sample takes one round of observations. The caller owns the clock, which
// keeps the whole gate testable without waiting out a real window.
func (r *Runner) Sample(ctx context.Context, now time.Time) {
	for _, t := range r.targets {
		for _, s := range r.scenarios {
			r.sampleOne(ctx, now, s, t)
		}
	}
}

func (r *Runner) sampleOne(ctx context.Context, now time.Time, s catalog.Scenario, t Target) {
	key := obsKey(s.ID, t.Params)
	o := r.obs[key]
	if o == nil {
		o = &Observation{
			Scenario:   s.ID,
			Params:     t.Params,
			Min:        math.Inf(1),
			Max:        math.Inf(-1),
			Threshold:  s.Signal.Threshold,
			Comparison: s.Signal.Comparison,
			For:        forDuration(s),
		}
		r.obs[key] = o
	}

	query, err := renderQuery(s.ID, s.Signal.PromQL, t.Params)
	if err != nil {
		if strings.Contains(err.Error(), "map has no entry for key") {
			o.Verdict = NotApplicable
			o.Note = "signal needs a parameter this target does not define"
			return
		}
		o.Errors++
		r.Log("%s: rendering signal: %v", key, err)
		return
	}

	value, err := r.querier.Query(ctx, query)
	switch {
	case errors.Is(err, prom.ErrNoData):
		// An empty result is not a value. Counting it as zero would hand a
		// perfect headroom score to an entry that measured nothing, which is
		// exactly the confusion this package refuses to make.
		o.Empty++
		r.endBreach(key, now, o)
		return
	case err != nil:
		o.Errors++
		r.Log("%s: query: %v", key, err)
		return
	}

	o.Samples++
	if value < o.Min {
		o.Min = value
	}
	if value > o.Max {
		o.Max = value
	}

	if breached(value, s.Signal.Comparison, s.Signal.Threshold) {
		if _, open := r.breachStart[key]; !open {
			r.breachStart[key] = now
		}
		if held := now.Sub(r.breachStart[key]); held > o.LongestBreach {
			o.LongestBreach = held
		}
		return
	}
	r.endBreach(key, now, o)
}

// endBreach closes any open breach interval for this key.
func (r *Runner) endBreach(key string, now time.Time, o *Observation) {
	start, open := r.breachStart[key]
	if !open {
		return
	}
	if held := now.Sub(start); held > o.LongestBreach {
		o.LongestBreach = held
	}
	delete(r.breachStart, key)
}

// Report finalizes every observation into a verdict, sorted by severity then
// by id so the output is stable and the worst entries read first.
func (r *Runner) Report() []Observation {
	out := make([]Observation, 0, len(r.obs))
	for _, o := range r.obs {
		out = append(out, r.judge(*o))
	}
	sort.Slice(out, func(i, j int) bool {
		if severity(out[i].Verdict) != severity(out[j].Verdict) {
			return severity(out[i].Verdict) > severity(out[j].Verdict)
		}
		if out[i].Scenario != out[j].Scenario {
			return out[i].Scenario < out[j].Scenario
		}
		return fmt.Sprint(out[i].Params) < fmt.Sprint(out[j].Params)
	})
	return out
}

// judge turns accumulated numbers into a verdict.
func (r *Runner) judge(o Observation) Observation {
	if o.Verdict == NotApplicable {
		return o
	}
	if o.Samples < r.cfg.MinSamples {
		o.Verdict = Unmeasured
		o.Headroom = math.NaN()
		switch {
		case o.Samples == 0 && o.Empty > 0:
			o.Note = fmt.Sprintf("no sample resolved (%d empty results): this entry's silence says nothing about its calibration", o.Empty)
		case o.Samples == 0 && o.Errors > 0:
			o.Note = fmt.Sprintf("no sample resolved (%d query errors)", o.Errors)
		case o.Samples == 0:
			o.Note = "no sample resolved"
		default:
			o.Note = fmt.Sprintf("only %d resolved samples, fewer than the %d required to judge", o.Samples, r.cfg.MinSamples)
		}
		return o
	}

	o.Headroom = headroom(o.Comparison, o.Min, o.Max, o.Threshold)

	switch {
	case o.LongestBreach >= o.For && o.LongestBreach > 0:
		o.Verdict = Miscalibrated
		o.Note = fmt.Sprintf("held past the threshold for %s on a healthy cluster, which is its full hold duration: this entry fires on health",
			o.LongestBreach.Round(time.Second))
	case o.For == 0 && o.LongestBreach > 0:
		// A zero hold duration fires on the first breaching sample.
		o.Verdict = Miscalibrated
		o.Note = "breached the threshold on a healthy cluster and has no hold duration, so it fires immediately"
	case o.LongestBreach > 0:
		o.Verdict = Marginal
		o.Note = fmt.Sprintf("crossed the threshold for %s on a healthy cluster without reaching its %s hold duration: it fires the moment a healthy day runs slightly longer",
			o.LongestBreach.Round(time.Second), o.For)
	case o.Headroom < r.cfg.MarginFactor:
		o.Verdict = Marginal
		o.Note = fmt.Sprintf("stayed quiet, but its healthy extreme is only %.2gx from the threshold", o.Headroom)
	default:
		o.Verdict = Calibrated
		o.Note = fmt.Sprintf("quiet with %.3gx headroom", o.Headroom)
	}
	return o
}

// headroom is how many times the healthy extreme must change to reach the
// threshold. The extreme that matters depends on the comparison: a
// greater-than entry is threatened by the maximum, a less-than entry by the
// minimum.
func headroom(comparison string, min, max, threshold float64) float64 {
	switch comparison {
	case ">", ">=":
		if max <= 0 {
			if threshold > 0 {
				return math.Inf(1)
			}
			return 0
		}
		return threshold / max
	case "<", "<=":
		if threshold <= 0 {
			return math.Inf(1)
		}
		if min <= 0 {
			return 0
		}
		return min / threshold
	}
	return math.NaN()
}

func severity(v Verdict) int {
	switch v {
	case Miscalibrated:
		return 4
	case Unmeasured:
		return 3
	case Marginal:
		return 2
	case Calibrated:
		return 1
	}
	return 0
}

// Summary counts verdicts across a report.
type Summary struct {
	Calibrated    int
	Marginal      int
	Miscalibrated int
	Unmeasured    int
	NotApplicable int
}

// Summarize counts a report's verdicts.
func Summarize(obs []Observation) Summary {
	var s Summary
	for _, o := range obs {
		switch o.Verdict {
		case Calibrated:
			s.Calibrated++
		case Marginal:
			s.Marginal++
		case Miscalibrated:
			s.Miscalibrated++
		case Unmeasured:
			s.Unmeasured++
		case NotApplicable:
			s.NotApplicable++
		}
	}
	return s
}

// Passed reports whether the catalog is fit to run against a real cluster.
//
// Marginal counts as a failure by default and that is deliberate. The whole
// reason this gate exists is that an entry sitting just under its threshold
// reads as passing on the day you test it, which is how the flapping entry
// this package was written for went unnoticed. Unmeasured also fails: an
// entry nobody could measure has not been shown to be safe, only quiet.
func (s Summary) Passed() bool {
	return s.Miscalibrated == 0 && s.Marginal == 0 && s.Unmeasured == 0
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
	d, _ := time.ParseDuration(s.Signal.For)
	return d
}

func obsKey(scenarioID string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(scenarioID)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%s", k, params[k])
	}
	return b.String()
}

// renderQuery fills a signal template with the target's parameters. A missing
// parameter is an error rather than an empty string, so an entry is never
// calibrated against a query with a hole in it.
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
