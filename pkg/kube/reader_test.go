package kube

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResourceArg(t *testing.T) {
	cases := []struct{ apiVersion, kind, want string }{
		{"apps/v1", "Deployment", "deployment.apps"},
		{"v1", "Service", "service"},
		{"security.istio.io/v1", "PeerAuthentication", "peerauthentication.security.istio.io"},
	}
	for _, c := range cases {
		if got := resourceArg(c.apiVersion, c.kind); got != c.want {
			t.Errorf("resourceArg(%s, %s) = %s, want %s", c.apiVersion, c.kind, got, c.want)
		}
	}
}

func deployment(t *testing.T) map[string]any {
	t.Helper()
	var obj map[string]any
	err := json.Unmarshal([]byte(`{
		"kind": "Deployment",
		"spec": {
			"replicas": 2,
			"template": {"spec": {"containers": [
				{"name": "app", "env": [
					{"name": "ERROR_RATE", "value": "0.9"},
					{"name": "TIMING_50_PERCENTILE", "value": "1200ms"},
					{"name": "FROM_SECRET", "valueFrom": {"secretKeyRef": {"name": "s"}}}
				]},
				{"name": "sidecar"}
			]}}
		}
	}`), &obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestExtractFieldEnvFanOut(t *testing.T) {
	got, err := ExtractField(deployment(t), "spec.template.spec.containers[*].env")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ERROR_RATE=0.9", "TIMING_50_PERCENTILE=1200ms", "FROM_SECRET"} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted %q, missing %q", got, want)
		}
	}
}

func TestExtractFieldScalarAndIndex(t *testing.T) {
	if got, _ := ExtractField(deployment(t), "spec.replicas"); got != "2" {
		t.Errorf("replicas = %q, want 2", got)
	}
	if got, _ := ExtractField(deployment(t), "spec.template.spec.containers[0].name"); got != "app" {
		t.Errorf("containers[0].name = %q, want app", got)
	}
}

func TestExtractFieldNoMatchIsError(t *testing.T) {
	if _, err := ExtractField(deployment(t), "spec.missing.path"); err == nil {
		t.Fatal("want error for a path that matches nothing")
	}
	if _, err := ExtractField(deployment(t), "spec.template.spec.containers[9].name"); err == nil {
		t.Fatal("want error for an out-of-range index")
	}
}

// TestGetThroughFakeKubectl runs Get against a stub kubectl script so the
// argument shape and JSON decoding are covered without a cluster.
func TestGetThroughFakeKubectl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub script is a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "kubectl")
	stub := `#!/bin/sh
echo "$@" > "` + dir + `/args"
echo '{"kind":"Deployment","metadata":{"name":"payments-v2"}}'
`
	if err := os.WriteFile(script, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Reader{bin: script}
	obj, err := r.Get(context.Background(), "apps/v1", "Deployment", "demo", "payments-v2")
	if err != nil {
		t.Fatal(err)
	}
	if md, _ := obj["metadata"].(map[string]any); md["name"] != "payments-v2" {
		t.Fatalf("decoded object %v, want payments-v2 metadata", obj)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	want := "get deployment.apps payments-v2 -o json -n demo"
	if strings.TrimSpace(string(args)) != want {
		t.Fatalf("kubectl args %q, want %q", strings.TrimSpace(string(args)), want)
	}

	// Name "*" lists the kind; the List envelope walks from items[*].
	if _, err := r.Get(context.Background(), "security.istio.io/v1", "PeerAuthentication", "demo", "*"); err != nil {
		t.Fatal(err)
	}
	args, err = os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	want = "get peerauthentication.security.istio.io -o json -n demo"
	if strings.TrimSpace(string(args)) != want {
		t.Fatalf("list-mode kubectl args %q, want %q", strings.TrimSpace(string(args)), want)
	}
}

func TestDiffTemplates(t *testing.T) {
	oldT := map[string]any{"spec": map[string]any{"args": []any{"curl http://payments:9090/"}}}
	newT := map[string]any{"spec": map[string]any{"args": []any{"curl http://payments-svc.demo:9090/"}}}
	diff := diffTemplates(oldT, newT)
	if !strings.Contains(diff, `- "curl http://payments:9090/"`) ||
		!strings.Contains(diff, `+ "curl http://payments-svc.demo:9090/"`) {
		t.Fatalf("diff missing changed lines:\n%s", diff)
	}
	if same := diffTemplates(oldT, oldT); !strings.Contains(same, "no template change") {
		t.Fatalf("identical templates should report no change, got %q", same)
	}
}

// triageStub answers the three triage kubectl shapes: deployment names,
// logs, and the replicaset list with two revisions of one deployment.
func triageStub(t *testing.T) *Reader {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub script is a shell script")
	}
	dir := t.TempDir()
	rsJSON := `{"items":[
	 {"metadata":{"creationTimestamp":"2026-07-16T00:00:00Z",
	   "ownerReferences":[{"kind":"Deployment","name":"loadgen"}],
	   "annotations":{"deployment.kubernetes.io/revision":"2"}},
	  "spec":{"template":{"spec":{"args":["new-target"]}}}},
	 {"metadata":{"creationTimestamp":"2026-07-16T00:00:00Z",
	   "ownerReferences":[{"kind":"Deployment","name":"loadgen"}],
	   "annotations":{"deployment.kubernetes.io/revision":"1"}},
	  "spec":{"template":{"spec":{"args":["old-target"]}}}}
	]}`
	if err := os.WriteFile(dir+"/rs.json", []byte(rsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// Both ReplicaSets are OLD (created yesterday) to simulate Kubernetes
	// reusing an existing RS on rollback: only the deployment's Progressing
	// lastUpdateTime says the rollout is fresh.
	deployJSON := `{"items":[
	 {"metadata":{"name":"loadgen"},
	  "status":{"conditions":[{"type":"Progressing","lastUpdateTime":"` +
		time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339) + `"}]}},
	 {"metadata":{"name":"payments-v1"},
	  "status":{"conditions":[{"type":"Progressing","lastUpdateTime":"2026-07-01T00:00:00Z"}]}}
	]}`
	if err := os.WriteFile(dir+"/deploy.json", []byte(deployJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
case "$*" in
  *jsonpath*) echo "loadgen payments-v1" ;;
  *replicasets*) cat "` + dir + `/rs.json" ;;
  *deployments*) cat "` + dir + `/deploy.json" ;;
  *logs*) echo 'curl: (6) Could not resolve host: payments-svc.demo'; echo 'ordinary line' ;;
esac
`
	script := dir + "/kubectl"
	if err := os.WriteFile(script, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Reader{bin: script}
}

func TestRecentRolloutsDiffsRevisions(t *testing.T) {
	r := triageStub(t)
	rollouts, err := r.RecentRollouts(context.Background(), "demo", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(rollouts) != 1 || rollouts[0].Deployment != "loadgen" {
		t.Fatalf("rollouts %+v, want one for loadgen", rollouts)
	}
	if !strings.Contains(rollouts[0].Diff, `- "old-target"`) || !strings.Contains(rollouts[0].Diff, `+ "new-target"`) {
		t.Fatalf("diff missing revision change:\n%s", rollouts[0].Diff)
	}
	if rollouts[0].AgeSeconds < 60 || rollouts[0].AgeSeconds > 300 {
		t.Fatalf("age %ds, want ~120s", rollouts[0].AgeSeconds)
	}
}

func TestDeploymentNamesAndLogs(t *testing.T) {
	r := triageStub(t)
	names, err := r.DeploymentNames(context.Background(), "demo")
	if err != nil || len(names) != 2 || names[0] != "loadgen" {
		t.Fatalf("names %v err %v, want [loadgen payments-v1]", names, err)
	}
	logs, err := r.Logs(context.Background(), "demo", "loadgen", 300, 200)
	if err != nil || !strings.Contains(logs, "Could not resolve host") {
		t.Fatalf("logs %q err %v, want resolver error line", logs, err)
	}
}
