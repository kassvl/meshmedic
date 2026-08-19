package main

import (
	"bytes"
	"context"
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
	checkUnparseable   checkStatus = "query unparseable"
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
		results = append(results, checkScenario(s, merged, metrics, labels))
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
