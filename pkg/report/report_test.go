package report

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
)

func TestMarkdownContainsTheStoryAndThePatch(t *testing.T) {
	inc := detect.Incident{
		Scenario: catalog.Scenario{
			ID:          "canary-latency-rollback",
			Title:       "Canary subset latency regression",
			Severity:    "critical",
			Description: "The canary is slow.",
			Signal:      catalog.Signal{Comparison: ">", Threshold: 1000, For: "90s"},
			Remediation: catalog.Remediation{Target: catalog.Target{Kind: "VirtualService"}},
			Guardrails:  catalog.Guardrails{RequiresApproval: true},
			Rollback:    "Restore previous weights.",
		},
		Params: map[string]string{"service": "payments", "namespace": "prod"},
		Value:  2412.7,
		Since:  time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		Evidence: []detect.EvidenceResult{
			{Name: "p99-stable-for-comparison", Samples: []prom.Sample{{Value: 84.2}}},
			{Name: "errors-by-workload", Samples: []prom.Sample{
				{Labels: map[string]string{"destination_workload": "payments-v1"}, Value: 0.002},
				{Labels: map[string]string{"destination_workload": "payments-v2"}, Value: 0.19},
			}},
			{Name: "canary-request-share", Err: errors.New("scrape failed")},
		},
	}
	out := Markdown(inc, "kind: VirtualService\n")

	for _, want := range []string{
		"Canary subset latency regression",
		"`namespace=prod` `service=payments`",
		"2413",
		"2026-07-17T12:00:00Z",
		"p99-stable-for-comparison | 84.2",
		`errors-by-workload{destination_workload="payments-v2"} | 0.19`,
		"canary-request-share | unavailable",
		"```yaml\nkind: VirtualService\n```",
		"Restore previous weights.",
		"requires human approval",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, out)
		}
	}

	// Triage sections render matched log lines and the rollout diff.
	inc.LogEvidence = []detect.LogEvidenceResult{{
		Name: "client-failure-log-sweep",
		Matches: map[string][]string{
			"loadgen": {"curl: (6) Could not resolve host: payments-svc.demo"},
		},
	}}
	inc.RolloutEvidence = []detect.RolloutEvidenceResult{{
		Name:     "recent-rollouts",
		Rollouts: []kube.Rollout{{Deployment: "loadgen", AgeSeconds: 120, Diff: "- old-target\n+ new-target"}},
	}}
	out = Markdown(inc, "kind: VirtualService\n")
	for _, want := range []string{
		"### Log evidence",
		"Could not resolve host: payments-svc.demo",
		"### Recent rollouts",
		"`loadgen` rolled 120s ago",
		"+ new-target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("triage markdown missing %q\n---\n%s", want, out)
		}
	}

	// The breakdown must lead with the offender: v2's row above v1's.
	v2 := strings.Index(out, `{destination_workload="payments-v2"}`)
	v1 := strings.Index(out, `{destination_workload="payments-v1"}`)
	if v2 == -1 || v1 == -1 || v2 > v1 {
		t.Errorf("evidence rows not ranked biggest-first (v2 at %d, v1 at %d)\n---\n%s", v2, v1, out)
	}
}
