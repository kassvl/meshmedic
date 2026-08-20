package proof

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/prom"
)

// scriptedProm answers each scenario's signal from a per-tick script, so a
// silence window can be driven without a cluster. The script is indexed by
// tick, and the last value repeats once it runs out.
type scriptedProm struct {
	values map[string][]float64
	tick   *int
}

func (s scriptedProm) at(id string) (float64, bool) {
	seq, ok := s.values[id]
	if !ok || len(seq) == 0 {
		return 0, false
	}
	i := *s.tick
	if i >= len(seq) {
		i = len(seq) - 1
	}
	return seq[i], true
}

func (s scriptedProm) Query(_ context.Context, promql string) (float64, error) {
	for id := range s.values {
		if strings.Contains(promql, id) {
			v, _ := s.at(id)
			return v, nil
		}
	}
	return 0, nil
}

// QuerySeries answers the coverage probe with one series so the silence run
// exercises the state machine rather than the probe, which has its own tests.
func (s scriptedProm) QuerySeries(_ context.Context, promql string) ([]prom.Sample, error) {
	return []prom.Sample{{Value: 1}}, nil
}

// silenceScenario builds an entry whose signal query embeds its own id.
func silenceScenario(id string, threshold float64, hold time.Duration) catalog.Scenario {
	return catalog.Scenario{
		ID:          id,
		Title:       id,
		Description: id,
		Signal: catalog.Signal{
			PromQL:     "signal_" + id,
			Comparison: ">",
			Threshold:  threshold,
			For:        hold.String(),
		},
		Remediation: catalog.Remediation{Action: "report-only"},
	}
}

// The three verdicts a silence window can reach, driven off one scripted run.
//
//   - never-breaches stays under its threshold: silent.
//   - brushes goes over for two ticks out of a five tick hold: brushed, and the
//     margin is the number that matters.
//   - fires goes over and stays over past a zero hold: a false positive.
func TestSilenceSeparatesQuietFromBrushedFromFired(t *testing.T) {
	tick := 0
	scenarios := []catalog.Scenario{
		silenceScenario("never-breaches", 100, 30*time.Second),
		silenceScenario("brushes", 100, 50*time.Second),
		silenceScenario("fires", 100, 0),
	}
	script := scriptedProm{
		tick: &tick,
		values: map[string][]float64{
			"never-breaches": {10, 20, 30, 40, 50, 60},
			"brushes":        {10, 150, 150, 10, 10, 10},
			"fires":          {10, 150, 150, 150, 150, 150},
		},
	}

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	r := newSilenceRunner(t, scenarios, script, &tick, &now)

	reports, err := r.Silence(context.Background(),
		[]detect.Target{{CoverageProbe: "up", Params: map[string]string{"namespace": "demo"}}},
		60*time.Second, 0)
	if err != nil {
		t.Fatalf("Silence: %v", err)
	}

	byEntry := map[string]SilenceReport{}
	for _, rep := range reports {
		byEntry[rep.Entry] = rep
	}

	if got := byEntry["never-breaches"].Verdict(); got != Silent {
		t.Errorf("never-breaches = %s, want silent", got)
	}
	if got := byEntry["never-breaches"].Peak; got != 60 {
		t.Errorf("never-breaches peak = %v, want 60", got)
	}
	if !math.IsInf(byEntry["never-breaches"].Margin(), 1) {
		t.Errorf("an entry that never breached should have infinite margin, got %v",
			byEntry["never-breaches"].Margin())
	}

	brushed := byEntry["brushes"]
	if got := brushed.Verdict(); got != Brushed {
		t.Errorf("brushes = %s, want brushed", got)
	}
	if brushed.Reports != 0 {
		t.Errorf("brushes reported %d incidents, want 0", brushed.Reports)
	}
	if brushed.LongestBreach != 20*time.Second {
		t.Errorf("brushes longest breach = %s, want 20s (two ticks at a 10s poll)", brushed.LongestBreach)
	}
	if note := brushed.Note(); !strings.Contains(note, "margin") {
		t.Errorf("a brushed entry must report its margin, got %q", note)
	}

	if got := byEntry["fires"].Verdict(); got != Fired {
		t.Errorf("fires = %s, want FIRED", got)
	}
	if byEntry["fires"].Reports == 0 {
		t.Error("fires should have delivered at least one incident")
	}
	if note := byEntry["fires"].Note(); !strings.Contains(note, "false positive") {
		t.Errorf("a firing entry on a clean cluster is a false positive and must say so, got %q", note)
	}
}

// Worst first: whoever reads one line of the output should read the reason the
// run failed, not an alphabetical accident.
func TestSilenceOrdersWorstFirst(t *testing.T) {
	tick := 0
	scenarios := []catalog.Scenario{
		silenceScenario("aaa-quiet", 100, 30*time.Second),
		silenceScenario("zzz-fires", 100, 0),
		silenceScenario("mmm-brushes", 100, 50*time.Second),
	}
	script := scriptedProm{
		tick: &tick,
		values: map[string][]float64{
			"aaa-quiet":   {1},
			"zzz-fires":   {150},
			"mmm-brushes": {150, 1, 1, 1, 1, 1},
		},
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	r := newSilenceRunner(t, scenarios, script, &tick, &now)

	reports, err := r.Silence(context.Background(),
		[]detect.Target{{CoverageProbe: "up", Params: map[string]string{"namespace": "demo"}}},
		60*time.Second, 0)
	if err != nil {
		t.Fatalf("Silence: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}
	if reports[0].Entry != "zzz-fires" {
		t.Errorf("first report is %s, want the entry that fired", reports[0].Entry)
	}
	if reports[1].Entry != "mmm-brushes" {
		t.Errorf("second report is %s, want the entry that brushed", reports[1].Entry)
	}
}

// A window measured across a suspended process is not a window. Silence is
// exactly what this command sells, so claiming to have watched a cluster that
// was not being watched is the one result it must never produce.
func TestSilenceRefusesAWindowThatWasSuspended(t *testing.T) {
	tick := 0
	scenarios := []catalog.Scenario{silenceScenario("quiet", 100, 30*time.Second)}
	script := scriptedProm{tick: &tick, values: map[string][]float64{"quiet": {1}}}

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	wall := now
	r := newSilenceRunner(t, scenarios, script, &tick, &now)
	// Monotonic time crawls while the wall clock jumps an hour: a closed lid.
	r.Wall = func() time.Time { return wall }
	r.Sleep = func(context.Context, time.Duration) {
		tick++
		now = now.Add(10 * time.Second)
		wall = wall.Add(time.Hour)
	}

	_, err := r.Silence(context.Background(),
		[]detect.Target{{CoverageProbe: "up", Params: map[string]string{"namespace": "demo"}}},
		60*time.Second, 0)
	if err == nil {
		t.Fatal("reported a silent window across an hour of suspension")
	}
	if !strings.Contains(err.Error(), "suspended") {
		t.Errorf("error should name suspension, got %v", err)
	}
}

// newSilenceRunner wires a Runner whose clock, sleep and querier are all
// scripted, so a window that takes a minute of wall time in production takes
// microseconds here. Each Sleep advances the tick the script is indexed by.
func newSilenceRunner(t *testing.T, scenarios []catalog.Scenario, script scriptedProm, tick *int, now *time.Time) *Runner {
	t.Helper()
	r := NewRunner(scenarios, script, nil)
	r.Poll = 10 * time.Second
	r.Now = func() time.Time { return *now }
	r.Wall = func() time.Time { return *now }
	r.Log = func(string, ...any) {}
	r.Sleep = func(context.Context, time.Duration) {
		*tick++
		*now = now.Add(r.Poll)
	}
	return r
}

// An entry whose signal needs a parameter the target does not define was never
// a question there. Reporting that as "no reading resolved, so this window
// says nothing about the entry" is the blind-versus-clear conflation this
// project exists to prevent, and the first version of this command committed
// it: an ingress entry on a plain service target came back as a coverage gap.
func TestSilenceSeparatesNotApplicableFromUnwatched(t *testing.T) {
	tick := 0
	needsParam := silenceScenario("ingress-only", 1, time.Minute)
	needsParam.Signal.PromQL = `signal_ingress-only{w="{{.ingress_workload}}"}`

	scenarios := []catalog.Scenario{
		silenceScenario("applies", 100, time.Minute),
		needsParam,
	}
	script := scriptedProm{tick: &tick, values: map[string][]float64{"applies": {1}}}

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	r := newSilenceRunner(t, scenarios, script, &tick, &now)

	reports, err := r.Silence(context.Background(),
		[]detect.Target{{CoverageProbe: "up", Params: map[string]string{"namespace": "demo"}}},
		60*time.Second, 0)
	if err != nil {
		t.Fatalf("Silence: %v", err)
	}

	byEntry := map[string]SilenceReport{}
	for _, rep := range reports {
		byEntry[rep.Entry] = rep
	}
	if got := byEntry["ingress-only"].Verdict(); got != NotApplicable {
		t.Errorf("ingress-only = %s, want n/a: it needs a parameter this target does not define", got)
	}
	if note := byEntry["ingress-only"].Note(); strings.Contains(note, "says nothing") {
		t.Errorf("a not-applicable pair must not read as a coverage gap, got %q", note)
	}
	if got := byEntry["applies"].Verdict(); got != Silent {
		t.Errorf("applies = %s, want silent", got)
	}
	// Ranked last: it is not a concern at all, so it must not push a real
	// finding down the page.
	if reports[len(reports)-1].Entry != "ingress-only" {
		t.Errorf("not-applicable should sort last, got %s", reports[len(reports)-1].Entry)
	}
}
