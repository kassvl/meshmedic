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
	if _, err := Render(scenarios[0], map[string]string{}); err == nil {
		t.Fatal("want error when params are missing, got nil")
	}
}
