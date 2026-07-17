// Package kube reads live objects through the kubectl binary, which every
// mesh operator already has. Keeping client-go out keeps the module
// dependency-free, and the surface read-only: `kubectl get -o json` plus
// field extraction is all the cluster access MeshMedic gets.
package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Reader fetches objects with one kubectl invocation each.
type Reader struct {
	bin string
}

// NewReader locates kubectl on PATH. Callers treat an error as "object
// evidence disabled", not as fatal: metrics detection works without it.
func NewReader() (*Reader, error) {
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, errors.New("kubectl not found on PATH")
	}
	return &Reader{bin: bin}, nil
}

// Get fetches one object as decoded JSON. An empty namespace queries a
// cluster-scoped resource. Name "*" lists the whole kind instead: kubectl
// returns a List envelope, so field paths start at items[*].
func (r *Reader) Get(ctx context.Context, apiVersion, kind, namespace, name string) (map[string]any, error) {
	args := []string{"get", resourceArg(apiVersion, kind)}
	if name != "*" {
		args = append(args, name)
	}
	args = append(args, "-o", "json")
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := exec.CommandContext(ctx, r.bin, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("kubectl get %s %s: %s", kind, name, bytes.TrimSpace(ee.Stderr))
		}
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		return nil, fmt.Errorf("kubectl get %s %s: decoding: %w", kind, name, err)
	}
	return obj, nil
}

// DeploymentNames lists the deployments in a namespace, for triage sweeps.
func (r *Reader) DeploymentNames(ctx context.Context, namespace string) ([]string, error) {
	out, err := r.run(ctx, "get", "deployments.apps", "-n", namespace,
		"-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(out))
	return fields, nil
}

// Logs returns recent log lines from one deployment's pods. Read-only, and
// bounded by tail and since so a chatty workload cannot flood a report.
func (r *Reader) Logs(ctx context.Context, namespace, deployment string, sinceSeconds, tailLines int) (string, error) {
	out, err := r.run(ctx, "logs", "deploy/"+deployment, "-n", namespace,
		"--tail", strconv.Itoa(tailLines), "--since", strconv.Itoa(sinceSeconds)+"s", "--prefix")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Rollout is one deployment's most recent template change: what rolled, how
// long ago, and the line-level diff between the previous and current pod
// template. The diff is where a bad deploy's root cause tends to sit.
type Rollout struct {
	Deployment string
	AgeSeconds int
	Diff       string
}

// RecentRollouts finds deployments in the namespace whose newest ReplicaSet
// is younger than within, and diffs its pod template against the previous
// revision's. A brand-new deployment (no prior revision) reports the whole
// template as added.
func (r *Reader) RecentRollouts(ctx context.Context, namespace string, within time.Duration) ([]Rollout, error) {
	out, err := r.run(ctx, "get", "replicasets.apps", "-n", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				OwnerReferences   []struct{ Kind, Name string } `json:"ownerReferences"`
				Annotations       map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Template any `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("kubectl get replicasets: decoding: %w", err)
	}

	type rs struct {
		revision int
		created  time.Time
		template any
	}
	byDeploy := map[string][]rs{}
	for _, item := range list.Items {
		owner := ""
		for _, o := range item.Metadata.OwnerReferences {
			if o.Kind == "Deployment" {
				owner = o.Name
			}
		}
		if owner == "" {
			continue
		}
		rev, _ := strconv.Atoi(item.Metadata.Annotations["deployment.kubernetes.io/revision"])
		byDeploy[owner] = append(byDeploy[owner], rs{rev, item.Metadata.CreationTimestamp, item.Spec.Template})
	}

	names := make([]string, 0, len(byDeploy))
	for name := range byDeploy {
		names = append(names, name)
	}
	sort.Strings(names)

	var rollouts []Rollout
	now := time.Now()
	for _, name := range names {
		sets := byDeploy[name]
		sort.Slice(sets, func(i, j int) bool { return sets[i].revision < sets[j].revision })
		newest := sets[len(sets)-1]
		age := now.Sub(newest.created)
		if age > within || newest.revision <= 0 {
			continue
		}
		var prevTemplate any
		if len(sets) > 1 {
			prevTemplate = sets[len(sets)-2].template
		}
		rollouts = append(rollouts, Rollout{
			Deployment: name,
			AgeSeconds: int(age.Seconds()),
			Diff:       diffTemplates(prevTemplate, newest.template),
		})
	}
	return rollouts, nil
}

// diffTemplates renders two pod templates as indented JSON and shows the
// lines that differ, unified-diff style. Naive line comparison is enough
// here: templates are small and the changed field is what matters.
func diffTemplates(oldT, newT any) string {
	oldLines := jsonLines(oldT)
	newLines := jsonLines(newT)
	oldSet := map[string]bool{}
	for _, l := range oldLines {
		oldSet[l] = true
	}
	newSet := map[string]bool{}
	for _, l := range newLines {
		newSet[l] = true
	}
	var b strings.Builder
	for _, l := range oldLines {
		if !newSet[l] {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range newLines {
		if !oldSet[l] {
			b.WriteString("+ " + l + "\n")
		}
	}
	if b.Len() == 0 {
		return "(no template change: restart or scale only)"
	}
	return strings.TrimRight(b.String(), "\n")
}

func jsonLines(v any) []string {
	if v == nil {
		return nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && l != "{" && l != "}" && l != "[" && l != "]" && l != "}," && l != "]," {
			out = append(out, l)
		}
	}
	return out
}

// run executes one kubectl invocation and returns stdout, folding stderr
// into the error the same way Get does.
func (r *Reader) run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, r.bin, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("kubectl %s: %s", strings.Join(args[:2], " "), bytes.TrimSpace(ee.Stderr))
		}
		return nil, err
	}
	return out, nil
}

// resourceArg maps apiVersion+kind to kubectl's kind.group form, which is
// unambiguous across API groups: (apps/v1, Deployment) -> deployment.apps,
// (v1, Service) -> service.
func resourceArg(apiVersion, kind string) string {
	group, _, ok := strings.Cut(apiVersion, "/")
	if !ok {
		return strings.ToLower(kind)
	}
	return strings.ToLower(kind) + "." + group
}

// ExtractField walks a dotted path like spec.template.spec.containers[*].env
// and renders what it finds. [i] indexes into an array, [*] fans out over
// one; multiple matches join with "; ". Scalars render bare, lists shaped
// like Kubernetes env entries render as NAME=value pairs, anything else
// structured renders as compact JSON. A path that matches nothing is an
// error so the report says "unavailable" instead of silently omitting it.
func ExtractField(obj map[string]any, path string) (string, error) {
	matches, err := walk(obj, strings.Split(path, "."))
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%s: no match", path)
	}
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, renderValue(m))
	}
	return strings.Join(parts, "; "), nil
}

func walk(v any, segs []string) ([]any, error) {
	if len(segs) == 0 {
		return []any{v}, nil
	}
	seg := segs[0]
	key, idx := seg, ""
	hasIdx := false
	if i := strings.IndexByte(seg, '['); i >= 0 && strings.HasSuffix(seg, "]") {
		key, idx, hasIdx = seg[:i], seg[i+1:len(seg)-1], true
	}
	if key != "" {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q: parent is not an object", key)
		}
		child, ok := m[key]
		if !ok {
			return nil, nil // absent field: no match, the fan-out may still find others
		}
		v = child
	}
	if hasIdx {
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("%q: not an array", seg)
		}
		if idx == "*" {
			var out []any
			for _, e := range arr {
				sub, err := walk(e, segs[1:])
				if err != nil {
					return nil, err
				}
				out = append(out, sub...)
			}
			return out, nil
		}
		n, err := strconv.Atoi(idx)
		if err != nil || n < 0 || n >= len(arr) {
			return nil, fmt.Errorf("%q: index out of range", seg)
		}
		return walk(arr[n], segs[1:])
	}
	return walk(v, segs[1:])
}

func renderValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return "null"
	case []any:
		if s, ok := renderEnvList(t); ok {
			return s
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// renderEnvList compacts a Kubernetes env-style list into NAME=value pairs,
// which is the shape configuration evidence usually takes. Entries without a
// plain value (valueFrom and friends) stay as compact JSON.
func renderEnvList(items []any) (string, bool) {
	if len(items) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return "", false
		}
		name, ok := m["name"].(string)
		if !ok {
			return "", false
		}
		if val, ok := m["value"].(string); ok {
			parts = append(parts, name+"="+val)
			continue
		}
		b, err := json.Marshal(m)
		if err != nil {
			return "", false
		}
		parts = append(parts, string(b))
	}
	return strings.Join(parts, " "), true
}
