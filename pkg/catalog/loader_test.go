package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRealCatalog(t *testing.T) {
	scenarios, err := LoadDir("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 12 {
		t.Fatalf("got %d scenarios, want 12", len(scenarios))
	}
	for i := 1; i < len(scenarios); i++ {
		if scenarios[i-1].ID >= scenarios[i].ID {
			t.Fatalf("scenarios not sorted by id: %q before %q", scenarios[i-1].ID, scenarios[i].ID)
		}
	}
	for _, s := range scenarios {
		if s.Rollback == "" {
			t.Errorf("%s: every scenario must document its rollback", s.ID)
		}
		if s.Signal.For == "" {
			t.Errorf("%s: signal.for is empty, transient blips would fire remediation", s.ID)
		}
	}
}

// writeScenarios lays out a temp catalog where each entry may suppress others.
func writeScenarios(t *testing.T, suppresses map[string][]string) string {
	t.Helper()
	dir := t.TempDir()
	for id, sup := range suppresses {
		doc := fmt.Sprintf(`id: %s
title: %s
signal:
  promql: up
  comparison: ">"
  threshold: 1
remediation:
  target:
    kind: VirtualService
  patchTemplate: "kind: VirtualService"
`, id, id)
		for _, s := range sup {
			doc += fmt.Sprintf("suppresses:\n  - %s\n", s)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadRejectsUnknownSuppressReference(t *testing.T) {
	dir := writeScenarios(t, map[string][]string{"a": {"ghost"}})
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("got %v, want unknown-reference error naming ghost", err)
	}
}

func TestLoadRejectsSelfSuppression(t *testing.T) {
	dir := writeScenarios(t, map[string][]string{"a": {"a"}})
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("got %v, want self-suppression error", err)
	}
}

func TestLoadRejectsMutualSuppression(t *testing.T) {
	dir := writeScenarios(t, map[string][]string{"a": {"b"}, "b": {"a"}})
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "each other") {
		t.Fatalf("got %v, want mutual-suppression error", err)
	}
}

func TestValidateRejectsBadComparison(t *testing.T) {
	s := Scenario{
		ID:    "x",
		Title: "x",
		Signal: Signal{PromQL: "up", Comparison: "~", Threshold: 1},
		Remediation: Remediation{
			Target:        Target{Kind: "VirtualService"},
			PatchTemplate: "kind: VirtualService",
		},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("want error for comparison ~, got nil")
	}
}

func TestValidateRejectsBrokenTemplate(t *testing.T) {
	s := Scenario{
		ID:    "x",
		Title: "x",
		Signal: Signal{PromQL: "up", Comparison: ">", Threshold: 1},
		Remediation: Remediation{
			Target:        Target{Kind: "VirtualService"},
			PatchTemplate: "kind: {{.unclosed",
		},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("want error for unparseable template, got nil")
	}
}
