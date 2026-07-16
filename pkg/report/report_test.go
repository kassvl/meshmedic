package report

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
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
			{Name: "p99-stable-for-comparison", Value: 84.2},
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
		"canary-request-share | unavailable",
		"```yaml\nkind: VirtualService\n```",
		"Restore previous weights.",
		"requires human approval",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, out)
		}
	}
}
