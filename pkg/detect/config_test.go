package detect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "watch.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaultsInterval(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
targets:
  - params:
      service: payments
      namespace: demo
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntervalDuration() != 30*time.Second {
		t.Fatalf("got %v, want default 30s", cfg.IntervalDuration())
	}
}

func TestLoadConfigRejectsMissingTargets(t *testing.T) {
	if _, err := LoadConfig(writeConfig(t, `prometheus: http://localhost:9090`)); err == nil {
		t.Fatal("want error for config without targets")
	}
}

func TestLoadConfigGitOpsDefaultsAndValidation(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
gitops:
  repo: kassvl/config
targets:
  - params:
      service: payments
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitOps.Path != "meshmedic/{{.namespace}}/{{.scenario}}.yaml" {
		t.Fatalf("got default path %q", cfg.GitOps.Path)
	}

	_, err = LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
gitops:
  repo: nomissingowner
targets:
  - params:
      service: payments
`))
	if err == nil {
		t.Fatal("want error for gitops.repo without owner/")
	}
}

func TestLoadConfigRejectsBadInterval(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
interval: soon
targets:
  - params:
      service: payments
`))
	if err == nil {
		t.Fatal("want error for unparseable interval")
	}
}

func TestConfigFromFlagsBuildsAWatchableConfig(t *testing.T) {
	cfg, err := ConfigFromFlags("http://prom:9090", "10s", []string{
		"service=payments,namespace=demo",
		"service=ledger, namespace=demo",
	})
	if err != nil {
		t.Fatalf("ConfigFromFlags: %v", err)
	}
	if cfg.Prometheus != "http://prom:9090" {
		t.Errorf("prometheus = %q", cfg.Prometheus)
	}
	if cfg.IntervalDuration() != 10*time.Second {
		t.Errorf("interval = %v, want 10s", cfg.IntervalDuration())
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(cfg.Targets))
	}
	// Whitespace around a pair is an ordinary thing to type and must not
	// become part of the key or the value.
	if got := cfg.Targets[1].Params["service"]; got != "ledger" {
		t.Errorf("second target service = %q, want ledger", got)
	}
	if got := cfg.Targets[1].Params["namespace"]; got != "demo" {
		t.Errorf("second target namespace = %q, want demo", got)
	}
}

func TestConfigFromFlagsRequiresPrometheusAndTarget(t *testing.T) {
	if _, err := ConfigFromFlags("", "", []string{"service=payments"}); err == nil {
		t.Error("expected an error with no prometheus URL")
	}
	if _, err := ConfigFromFlags("http://prom:9090", "", nil); err == nil {
		t.Error("expected an error with no targets")
	}
	if _, err := ConfigFromFlags("http://prom:9090", "nope", []string{"service=payments"}); err == nil {
		t.Error("expected an error on an unparseable interval")
	}
}

func TestParseTargetSpecRejectsMalformedInput(t *testing.T) {
	for _, spec := range []string{"bogus", "=payments", "", ",,", "service=a,service=b"} {
		if _, err := ParseTargetSpec(spec); err == nil {
			t.Errorf("ParseTargetSpec(%q) = nil error, want a rejection", spec)
		}
	}
	// An empty value is legitimate: a param can be deliberately blank.
	params, err := ParseTargetSpec("service=payments,subset=")
	if err != nil {
		t.Fatalf("ParseTargetSpec: %v", err)
	}
	if params["subset"] != "" || len(params) != 2 {
		t.Errorf("params = %v", params)
	}
}
