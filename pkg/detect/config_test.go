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
