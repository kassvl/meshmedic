package remediate

import (
	"strings"
	"testing"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

// fullParams is the complete parameter vocabulary catalog templates may use.
// Adding a new key here means every scenario can rely on it at runtime.
var fullParams = map[string]string{
	"service":       "payments",
	"namespace":     "prod",
	"workload":      "payments-v2",
	"subset":        "v2",
	"stable_subset": "v1",
}

func TestRenderEveryCatalogEntry(t *testing.T) {
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scenarios {
		out, err := Render(s, fullParams)
		if err != nil {
			t.Errorf("%s: %v", s.ID, err)
			continue
		}
		if s.Remediation.Action == "report-only" {
			if !strings.Contains(out, "report-only") {
				t.Errorf("%s: report-only placeholder missing", s.ID)
			}
			continue
		}
		if !strings.Contains(out, "prod") {
			t.Errorf("%s: namespace parameter not substituted into patch", s.ID)
		}
		if strings.Contains(out, "{{") {
			t.Errorf("%s: unrendered template markers left in patch", s.ID)
		}
	}
}

func TestRenderMissingParamFails(t *testing.T) {
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	// Pick a scenario that actually renders a patch: report-only entries
	// return a fixed placeholder and never touch params, so they cannot
	// surface a missing-param error.
	var patchScenario *catalog.Scenario
	for i := range scenarios {
		if scenarios[i].Remediation.Action != "report-only" {
			patchScenario = &scenarios[i]
			break
		}
	}
	if patchScenario == nil {
		t.Fatal("no patch-rendering scenario in the catalog to test against")
	}
	if _, err := Render(*patchScenario, map[string]string{}); err == nil {
		t.Fatalf("want error when params are missing for %s, got nil", patchScenario.ID)
	}
}
