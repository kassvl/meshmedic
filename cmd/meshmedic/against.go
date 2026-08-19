package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"text/template"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/prom"
	"github.com/kassvl/meshmedic/pkg/promql"
)

// Label sets were verified live on one Istio version. When Istio renames a
// label, the affected PromQL silently never fires: a matcher on a label that
// no longer exists matches nothing rather than erroring, so the coverage is
// lost with no signal at all and is discovered during an incident, which is
// the worst possible time. This checks the catalog against a live Prometheus
// before that happens.

// checkStatus is one scenario's standing against a live Prometheus.
type checkStatus string

const (
	checkOK            checkStatus = "ok"
	checkMetricMissing checkStatus = "metric missing"
	checkLabelMissing  checkStatus = "label missing"
	// checkNoSeries is the subtler failure, and the one that only shows up
	// against a real cluster: every name the query references exists, and the
	// selector still matches nothing. retry-storm-damping is the live
	// example. envoy_cluster_upstream_rq_retry is present with 5 series, all
	// of them cluster_name="xds-grpc" (a proxy's own connection to istiod),
	// so the entry's outbound-cluster selector matches nothing and the entry
	// can never fire. A name-level check calls that healthy.
	checkNoSeries    checkStatus = "selector matches nothing"
	checkUnparseable checkStatus = "query unparseable"
)

type checkResult struct {
	Scenario string
	Status   checkStatus
	Missing  []string // the names that were not found
}

// checkParams are the template parameters used to render catalog queries for
// checking. They only have to be plausible identifiers: the check is about
// which metrics and labels a query names, not which target it names. Real
// target params override them when a config is supplied.
var checkParams = map[string]string{
	"service": "payments", "namespace": "demo", "workload": "payments-v2",
	"subset": "v2", "stable_subset": "v1", "ingress_workload": "ingress-istio",
}

// runAgainstPrometheus checks every catalog entry's metric and label
// references against a live server, and reports per scenario.
func runAgainstPrometheus(scenarios []catalog.Scenario, promURL string, params map[string]string, strict bool) {
	client := prom.NewClient(promURL)
	ctx := context.Background()
	_ = ctx

	metrics, err := client.MetricNames(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading metric names from %s: %v\n", promURL, err)
		os.Exit(1)
	}
	labels, err := client.LabelNames(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading label names from %s: %v\n", promURL, err)
		os.Exit(1)
	}
	if len(metrics) == 0 {
		fmt.Fprintf(os.Stderr, "%s knows no metric names at all; checking against it would call every entry broken\n", promURL)
		os.Exit(1)
	}

	merged := map[string]string{}
	for k, v := range checkParams {
		merged[k] = v
	}
	for k, v := range params {
		merged[k] = v
	}

	results := make([]checkResult, 0, len(scenarios))
	for _, s := range scenarios {
		r := checkScenario(s, merged, metrics, labels)
		// Names existing is necessary but not sufficient. Only run the
		// selector check when the names are fine, so the report names the
		// first reason a query is dead rather than the last.
		if r.Status == checkOK {
			r = checkSeries(ctx, client, s, merged, r)
		}
		results = append(results, r)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCHECK\tMISSING")
	broken := 0
	for _, r := range results {
		missing := "-"
		if len(r.Missing) > 0 {
			missing = strings.Join(r.Missing, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Scenario, r.Status, missing)
		if r.Status != checkOK {
			broken++
		}
	}
	w.Flush()

	fmt.Printf("\n%d of %d entries resolve against %s\n", len(results)-broken, len(results), promURL)
	if broken > 0 {
		fmt.Printf("%d entries reference names this Prometheus does not have. "+
			"They cannot fire, and nothing about their silence would tell you so.\n", broken)
	}
	if strict && broken > 0 {
		os.Exit(1)
	}
}

// checkScenario resolves one entry's signal and evidence queries. The signal
// is what decides the verdict: an evidence query naming a missing metric
// degrades the report, but a signal naming one means the entry is dead.
func checkScenario(s catalog.Scenario, params map[string]string, metrics, labels map[string]bool) checkResult {
	r := checkResult{Scenario: s.ID, Status: checkOK}

	query, err := renderForCheck(s.ID, s.Signal.PromQL, params)
	if err != nil {
		// A signal needing a param no target defines is not checkable here;
		// that is a targeting question, not a telemetry one.
		r.Status = checkUnparseable
		r.Missing = []string{"signal template: " + short(err.Error())}
		return r
	}
	refs, err := promql.Extract(query)
	if err != nil {
		r.Status = checkUnparseable
		r.Missing = []string{short(err.Error())}
		return r
	}

	var missingMetrics, missingLabels []string
	for _, m := range refs.Metrics {
		if !metrics[m] {
			missingMetrics = append(missingMetrics, m)
		}
	}
	for _, l := range refs.Labels {
		// `le` is a histogram bucket label that only exists on bucket series;
		// Prometheus lists it, but a server with no histograms will not, and
		// that is not the entry's fault.
		if !labels[l] {
			missingLabels = append(missingLabels, l)
		}
	}
	sort.Strings(missingMetrics)
	sort.Strings(missingLabels)

	switch {
	case len(missingMetrics) > 0:
		r.Status = checkMetricMissing
		r.Missing = missingMetrics
	case len(missingLabels) > 0:
		r.Status = checkLabelMissing
		r.Missing = missingLabels
	}
	return r
}

// checkSeries asks whether the entry's metric carries any series for this
// target at all, which is a different question from whether the entry is
// firing right now.
//
// The distinction matters and only surfaces against a real cluster. On a
// healthy mesh almost every entry's full signal returns nothing, because the
// failure it looks for is not happening: authz-deny-flood selects
// response_code="403" and a healthy service emits none. That is the cluster
// being well, not the entry being broken. Meanwhile retry-storm-damping
// selects envoy_cluster_upstream_rq_retry for an outbound cluster, and while
// that metric does exist, every one of its series is cluster_name="xds-grpc",
// a proxy's own connection to istiod. It matches nothing and never will.
//
// The two are told apart by keeping only the matchers whose values came from
// the target's own parameters, and dropping the ones that describe a failure.
// What is left must resolve on a healthy cluster; if it does not, the entry
// is structurally dead for this target.
func checkSeries(ctx context.Context, client *prom.Client, s catalog.Scenario, params map[string]string, r checkResult) checkResult {
	if s.Signal.AbsenceIsSignal {
		return r
	}
	query, err := renderForCheck(s.ID+"/series", s.Signal.PromQL, params)
	if err != nil {
		return r
	}
	refs, err := promql.Extract(query)
	if err != nil || len(refs.Metrics) == 0 {
		return r
	}

	paramValues := map[string]bool{}
	for _, v := range params {
		if v != "" {
			paramValues[v] = true
		}
	}

	for _, metric := range refs.Metrics {
		probe := targetProbe(metric, refs.Matchers, paramValues)
		samples, err := client.QuerySeries(ctx, probe)
		if errors.Is(err, prom.ErrNoData) || (err == nil && len(samples) == 0) {
			r.Status = checkNoSeries
			r.Missing = []string{fmt.Sprintf("%s carries no series for %s", metric, describeTarget(params))}
			return r
		}
	}
	return r
}

// identifiesTarget reports whether a matcher names the target rather than the
// failure. An equality matcher does when its value is one of the target's
// parameters. A regex matcher does when it was built from them: the retry
// entry selects cluster_name=~"outbound\\|.*\\|payments\\.demo\\..*", which
// is neither equal to a parameter nor a failure condition, and dropping it
// would leave a probe so broad that the entry looks healthy on the strength
// of a proxy's own xds-grpc series.
func identifiesTarget(m promql.Matcher, paramValues map[string]bool) bool {
	switch m.Op {
	case "=":
		return paramValues[m.Value]
	case "=~":
		for v := range paramValues {
			if strings.Contains(m.Value, v) {
				return true
			}
		}
	}
	return false
}

// targetProbe builds `count({__name__="metric", <target matchers>})`: the
// entry's metric narrowed to this target and nothing else. Matchers whose
// value is one of the target's own parameters identify the target; every
// other matcher describes the failure and is dropped, because a healthy
// cluster is supposed to fail those.
func targetProbe(metric string, matchers []promql.Matcher, paramValues map[string]bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `count({__name__=%q`, metric)
	seen := map[string]bool{}
	for _, m := range matchers {
		if seen[m.Label] || !identifiesTarget(m, paramValues) {
			continue
		}
		seen[m.Label] = true
		fmt.Fprintf(&b, ", %s%s%q", m.Label, m.Op, m.Value)
	}
	b.WriteString("})")
	return b.String()
}

func renderForCheck(name, tmpl string, params map[string]string) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, params); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func short(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
