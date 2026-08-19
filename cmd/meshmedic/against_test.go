package main

import (
	"testing"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

func istioMetrics() map[string]bool {
	return map[string]bool{
		"istio_requests_total":                       true,
		"istio_request_duration_milliseconds_bucket": true,
		"istio_tcp_connections_closed_total":         true,
	}
}

func istioLabels() map[string]bool {
	return map[string]bool{
		"le": true, "reporter": true, "response_code": true, "response_flags": true,
		"destination_service_name": true, "destination_service_namespace": true,
		"destination_workload": true, "destination_version": true,
		"source_workload_namespace": true, "source_app": true, "source_workload": true,
	}
}

// The whole shipped catalog, checked against the metric and label set the
// stock Istio Prometheus addon actually scrapes. Two entries reference
// families it does not scrape, and this is the machine-checked evidence for
// that, independent of the lock which records it from documentation.
func TestShippedCatalogAgainstStockIstioTelemetry(t *testing.T) {
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	metrics, labels := istioMetrics(), istioLabels()

	knownBroken := map[string]string{
		"retry-storm-damping":     "envoy_cluster_upstream_rq_retry",
		"waypoint-overload-scale": "kube_pod_status_ready",
	}
	for _, s := range scenarios {
		r := checkScenario(s, checkParams, metrics, labels)
		want, broken := knownBroken[s.ID]
		switch {
		case broken && r.Status != checkMetricMissing:
			t.Errorf("%s: status %q, want %q (%s is not scraped by the stock addon)",
				s.ID, r.Status, checkMetricMissing, want)
		case broken && (len(r.Missing) != 1 || r.Missing[0] != want):
			t.Errorf("%s: missing %v, want [%s]", s.ID, r.Missing, want)
		case !broken && r.Status != checkOK:
			t.Errorf("%s: status %q missing %v, want ok", s.ID, r.Status, r.Missing)
		}
	}
}

// The failure this check exists for: Istio renames a label, and the affected
// PromQL silently never fires again. It must flag exactly the entries that
// use the label, not all of them and not none.
func TestALabelRenameFlagsExactlyTheAffectedEntries(t *testing.T) {
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	labels := istioLabels()
	delete(labels, "response_flags")

	var flagged, clean int
	for _, s := range scenarios {
		usesFlag := false
		if q, err := renderForCheck(s.ID, s.Signal.PromQL, checkParams); err == nil {
			usesFlag = contains(q, "response_flags")
		}
		r := checkScenario(s, checkParams, istioMetrics(), labels)
		switch {
		case usesFlag && r.Status == checkLabelMissing:
			flagged++
		case usesFlag:
			t.Errorf("%s uses response_flags but checked %q", s.ID, r.Status)
		case r.Status == checkLabelMissing:
			t.Errorf("%s does not use response_flags but was flagged for it", s.ID)
		default:
			clean++
		}
	}
	if flagged == 0 {
		t.Fatal("no entry was flagged; the label check is not doing anything")
	}
	t.Logf("%d entries flagged, %d unaffected", flagged, clean)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
