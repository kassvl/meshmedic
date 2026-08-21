package proof

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

// remediableEntry is an entry that proposes a patch, so there is something to
// prove. Its signal query embeds its id so the scripted querier can drive it.
func remediableEntry(id string) catalog.Scenario {
	return catalog.Scenario{
		ID:          id,
		Title:       id,
		Description: id,
		Signal: catalog.Signal{
			PromQL:     "signal_" + id,
			Comparison: ">",
			Threshold:  100,
		},
		Remediation: catalog.Remediation{
			Target:        catalog.Target{APIVersion: "networking.istio.io/v1", Kind: "DestinationRule"},
			Action:        "enable-outlier-detection",
			PatchTemplate: "kind: DestinationRule\nmetadata:\n  namespace: {{.namespace}}\n",
		},
	}
}

func remediationSpec(entry string) Spec {
	return Spec{
		Entry:       entry,
		Summary:     "a fault and the fix it proposes",
		Target:      map[string]string{"namespace": "demo"},
		Inject:      []Command{{Run: []string{"kubectl", "--context=kind-test", "set", "env", "x"}}},
		Reset:       []Command{{Run: []string{"kubectl", "reset-fault"}}},
		FiresWithin: Duration(time.Minute),
		Expect:      Expect{Names: []string{"x"}},
		Remediation: &RemediationCheck{
			ClearsWithin: Duration(time.Minute),
			Revert:       []Command{{Run: []string{"kubectl", "delete", "the-patch"}}},
		},
	}
}

// recorder captures every command and the bytes handed to stdin, so a test can
// assert on what would actually have reached the cluster.
type recorder struct {
	argv   [][]string
	stdin  []string
	failOn string
}

func (rec *recorder) exec(_ context.Context, argv []string) error {
	rec.argv = append(rec.argv, argv)
	if rec.failOn != "" && strings.Contains(strings.Join(argv, " "), rec.failOn) {
		return errors.New("scripted failure")
	}
	return nil
}

func (rec *recorder) execInput(_ context.Context, argv []string, stdin string) error {
	rec.argv = append(rec.argv, argv)
	rec.stdin = append(rec.stdin, stdin)
	if rec.failOn != "" && strings.Contains(strings.Join(argv, " "), rec.failOn) {
		return errors.New("scripted failure")
	}
	return nil
}

func newRemediationRunner(t *testing.T, scenarios []catalog.Scenario, script scriptedProm, rec *recorder, tick *int, now *time.Time) *Runner {
	t.Helper()
	r := NewRunner(scenarios, script, nil)
	r.Poll = 10 * time.Second
	r.Now = func() time.Time { return *now }
	r.Wall = func() time.Time { return *now }
	r.Log = func(string, ...any) {}
	r.Exec = rec.exec
	r.ExecInput = rec.execInput
	r.Sleep = func(_ context.Context, d time.Duration) {
		*tick++
		*now = now.Add(d)
	}
	return r
}

// The claim under test: the patch is applied while the fault is still in
// place, and the signal recovers anyway. Removing the fault first would prove
// only that the fault caused the incident, which the ordinary proof covers.
func TestRemediationProvesTheFixWorksWithTheFaultStillThere(t *testing.T) {
	tick := 0
	entry := remediableEntry("error-surge")
	// Breaches for three ticks, then recovers: the patch lands on tick 3.
	script := scriptedProm{tick: &tick, values: map[string][]float64{
		"error-surge": {150, 150, 150, 20, 20, 20},
	}}
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := newRemediationRunner(t, []catalog.Scenario{entry}, script, rec, &tick, &now)

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !res.Passed() {
		t.Fatalf("expected a pass, got %s: %v", res.Verdict(), res.Failures)
	}
	if !res.Fired || !res.Applied || !res.Cleared {
		t.Errorf("fired=%v applied=%v cleared=%v, want all true", res.Fired, res.Applied, res.Cleared)
	}
	if res.AtEnd >= res.Threshold {
		t.Errorf("finished at %v against a threshold of %v", res.AtEnd, res.Threshold)
	}

	// The fault's own reset must not have run before the patch was applied,
	// or the recovery measured the reset rather than the remedy.
	var applyAt, resetAt = -1, -1
	for i, argv := range rec.argv {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "apply") {
			applyAt = i
		}
		if strings.Contains(joined, "reset-fault") && resetAt < 0 {
			resetAt = i
		}
	}
	if applyAt < 0 {
		t.Fatal("the patch was never applied")
	}
	if resetAt >= 0 && resetAt < applyAt {
		t.Error("the fault was reset before the patch was applied, so the recovery proves nothing about the patch")
	}
}

// The bytes applied must be the bytes the entry proposes, rendered with the
// target's own parameters. A proof that applies something else has proven
// something else.
func TestRemediationAppliesTheEntrysOwnRenderedPatch(t *testing.T) {
	tick := 0
	entry := remediableEntry("error-surge")
	script := scriptedProm{tick: &tick, values: map[string][]float64{"error-surge": {150, 20}}}
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := newRemediationRunner(t, []catalog.Scenario{entry}, script, rec, &tick, &now)

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))
	if len(rec.stdin) != 1 {
		t.Fatalf("stdin was written %d times, want once", len(rec.stdin))
	}
	if !strings.Contains(rec.stdin[0], "namespace: demo") {
		t.Errorf("the patch was not rendered with the target's parameters:\n%s", rec.stdin[0])
	}
	if rec.stdin[0] != res.Patch {
		t.Error("the recorded patch and the applied bytes differ, so the result is not auditable")
	}
	if strings.Contains(rec.stdin[0], "{{") {
		t.Errorf("an unrendered template reached the cluster:\n%s", rec.stdin[0])
	}
}

// A patch that does not stop the incident is the finding this command exists
// to produce, and the failure has to say so in those terms.
func TestRemediationFailsWhenTheFixDoesNotFix(t *testing.T) {
	tick := 0
	entry := remediableEntry("error-surge")
	script := scriptedProm{tick: &tick, values: map[string][]float64{"error-surge": {150}}} // never recovers
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := newRemediationRunner(t, []catalog.Scenario{entry}, script, rec, &tick, &now)

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))
	if res.Passed() {
		t.Fatal("a patch that changed nothing was reported as a pass")
	}
	if !res.Applied {
		t.Error("the patch should still have been applied")
	}
	joined := strings.Join(res.Failures, " ")
	if !strings.Contains(joined, "did not stop it") {
		t.Errorf("the failure should say the proposed change did not stop the incident, got: %v", res.Failures)
	}
}

// Thirteen of eighteen entries are report-only. Skipping them is the correct
// answer, not a coverage gap, and it must not read as one.
func TestRemediationSkipsReportOnlyEntries(t *testing.T) {
	tick := 0
	entry := remediableEntry("triage")
	entry.Remediation.Action = "report-only"
	entry.Remediation.PatchTemplate = ""
	script := scriptedProm{tick: &tick, values: map[string][]float64{"triage": {150}}}
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := newRemediationRunner(t, []catalog.Scenario{entry}, script, rec, &tick, &now)

	res := r.Remediate(context.Background(), remediationSpec("triage"))
	if res.Skipped == "" {
		t.Fatal("a report-only entry should be skipped, not attempted")
	}
	if res.Verdict() != "skipped" {
		t.Errorf("verdict = %s, want skipped", res.Verdict())
	}
	if len(rec.argv) != 0 {
		t.Errorf("a skipped entry touched the cluster: %v", rec.argv)
	}
}

// The revert runs on failure too. A run that leaves its own remediation behind
// has quietly reconfigured the mesh for every measurement after it, and unlike
// a leftover fault a leftover fix is silent.
func TestRemediationRevertsEvenWhenTheProofFails(t *testing.T) {
	tick := 0
	entry := remediableEntry("error-surge")
	script := scriptedProm{tick: &tick, values: map[string][]float64{"error-surge": {150}}}
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := newRemediationRunner(t, []catalog.Scenario{entry}, script, rec, &tick, &now)

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))
	if res.Passed() {
		t.Fatal("this run was supposed to fail")
	}
	if !res.Reverted {
		t.Error("the patch was left on the cluster after a failed run")
	}
	var sawRevert, sawReset bool
	for _, argv := range rec.argv {
		joined := strings.Join(argv, " ")
		sawRevert = sawRevert || strings.Contains(joined, "delete the-patch")
		sawReset = sawReset || strings.Contains(joined, "reset-fault")
	}
	if !sawRevert || !sawReset {
		t.Errorf("revert=%v reset=%v, want both after a failed run", sawRevert, sawReset)
	}
}

// An entry that never breached has no incident to remedy, and applying a patch
// to a healthy cluster would prove nothing while changing it.
func TestRemediationDoesNotPatchAClusterThatIsFine(t *testing.T) {
	tick := 0
	entry := remediableEntry("error-surge")
	script := scriptedProm{tick: &tick, values: map[string][]float64{"error-surge": {5}}}
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := newRemediationRunner(t, []catalog.Scenario{entry}, script, rec, &tick, &now)

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))
	if res.Applied {
		t.Error("a patch was applied to a cluster where nothing was wrong")
	}
	if len(rec.stdin) != 0 {
		t.Errorf("something was piped to the cluster anyway: %v", rec.stdin)
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "no incident to remedy") {
		t.Errorf("the failure should say there was no incident, got: %v", res.Failures)
	}
}

// The patch has to land in the cluster the fault was injected into. A
// remediation applied through a different context is a change to somebody
// else's mesh.
func TestRemediationAppliesThroughTheSameContextAsTheFault(t *testing.T) {
	tick := 0
	entry := remediableEntry("error-surge")
	script := scriptedProm{tick: &tick, values: map[string][]float64{"error-surge": {150, 20}}}
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := newRemediationRunner(t, []catalog.Scenario{entry}, script, rec, &tick, &now)

	r.Remediate(context.Background(), remediationSpec("error-surge"))
	var applyArgv []string
	for _, argv := range rec.argv {
		if strings.Contains(strings.Join(argv, " "), "apply") {
			applyArgv = argv
		}
	}
	if applyArgv == nil {
		t.Fatal("no apply command was run")
	}
	if !strings.Contains(strings.Join(applyArgv, " "), "--context=kind-test") {
		t.Errorf("the patch was applied without the fault's context: %v", applyArgv)
	}
}
