package catalog

import "testing"

func TestLoadRealCatalog(t *testing.T) {
	scenarios, err := LoadDir("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 7 {
		t.Fatalf("got %d scenarios, want 7", len(scenarios))
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
