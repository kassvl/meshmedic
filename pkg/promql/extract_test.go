package promql

import (
	"reflect"
	"testing"
)

func TestExtractRealCatalogSignals(t *testing.T) {
	cases := []struct {
		name        string
		query       string
		wantMetrics []string
		wantLabels  []string
	}{
		{
			name:        "simple selector",
			query:       `sum(rate(istio_requests_total{reporter="waypoint", response_code="403"}[2m]))`,
			wantMetrics: []string{"istio_requests_total"},
			wantLabels:  []string{"reporter", "response_code"},
		},
		{
			// The case a regex gets wrong: a brace inside a string literal.
			name:        "braces inside a string literal are not a selector",
			query:       `count(kube_pod_status_ready{namespace="demo", pod=~"payments-{v2}-.*", condition="true"})`,
			wantMetrics: []string{"kube_pod_status_ready"},
			wantLabels:  []string{"condition", "namespace", "pod"},
		},
		{
			name:        "grouping labels are label references too",
			query:       `histogram_quantile(0.99, sum by (le, destination_workload) (rate(istio_request_duration_milliseconds_bucket{reporter="waypoint"}[2m])))`,
			wantMetrics: []string{"istio_request_duration_milliseconds_bucket"},
			wantLabels:  []string{"destination_workload", "le", "reporter"},
		},
		{
			name: "a ratio references the same metric twice",
			query: `sum(rate(istio_requests_total{response_code=~"5.."}[2m])) ` +
				`/ sum(rate(istio_requests_total{}[2m]))`,
			wantMetrics: []string{"istio_requests_total"},
			wantLabels:  []string{"response_code"},
		},
		{
			// The absence signal: bool operators, offset, nested subquery.
			name: "absence signal with bool and subquery",
			query: `((sum(rate(istio_requests_total{reporter="waypoint"}[2m])) or vector(0)) < bool 0.05) ` +
				`* (max_over_time((sum(rate(istio_requests_total{reporter="waypoint"}[2m])) or vector(0))[30m:1m]) > bool 0.5)`,
			wantMetrics: []string{"istio_requests_total"},
			wantLabels:  []string{"reporter"},
		},
		{
			name:        "escaped regex in a matcher",
			query:       `sum(rate(envoy_cluster_upstream_rq_retry{cluster_name=~"outbound\\|.*\\|payments\\.demo\\..*"}[2m]))`,
			wantMetrics: []string{"envoy_cluster_upstream_rq_retry"},
			wantLabels:  []string{"cluster_name"},
		},
		{
			name:        "__name__ matcher names a metric",
			query:       `sum({__name__="istio_tcp_connections_closed_total", response_flags="DENY"})`,
			wantMetrics: []string{"istio_tcp_connections_closed_total"},
			wantLabels:  []string{"response_flags"},
		},
		{
			name:        "bare metric with no selector",
			query:       `up`,
			wantMetrics: []string{"up"},
			wantLabels:  nil,
		},
		{
			name:        "comments are ignored",
			query:       "# the ratio that matters\nsum(rate(istio_requests_total[2m]))",
			wantMetrics: []string{"istio_requests_total"},
			wantLabels:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Extract(tc.query)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !reflect.DeepEqual(got.Metrics, tc.wantMetrics) {
				t.Errorf("metrics = %v, want %v", got.Metrics, tc.wantMetrics)
			}
			if len(got.Labels) == 0 && len(tc.wantLabels) == 0 {
				return
			}
			if !reflect.DeepEqual(got.Labels, tc.wantLabels) {
				t.Errorf("labels = %v, want %v", got.Labels, tc.wantLabels)
			}
		})
	}
}

// Function names are not metrics. Getting this wrong would report every
// catalog entry as depending on a metric called "rate".
func TestFunctionNamesAreNotMetrics(t *testing.T) {
	got, err := Extract(`histogram_quantile(0.99, sum by (le) (rate(istio_request_duration_milliseconds_bucket[2m])))`)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, m := range got.Metrics {
		switch m {
		case "histogram_quantile", "sum", "rate", "le":
			t.Errorf("%q reported as a metric name", m)
		}
	}
	if len(got.Metrics) != 1 || got.Metrics[0] != "istio_request_duration_milliseconds_bucket" {
		t.Errorf("metrics = %v, want just the bucket metric", got.Metrics)
	}
}

func TestUnterminatedStringIsAnError(t *testing.T) {
	if _, err := Extract(`sum(istio_requests_total{reporter="waypoint})`); err == nil {
		t.Error("accepted a query with an unterminated string literal")
	}
}

// Every signal and evidence query in the shipped catalog must extract without
// error, or the checker cannot be trusted to report on them.
func TestEveryCatalogQueryExtracts(t *testing.T) {
	for _, q := range catalogQueries(t) {
		if _, err := Extract(q.query); err != nil {
			t.Errorf("%s: %v\n  %s", q.where, err, q.query)
		}
	}
}
