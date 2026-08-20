package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Outcome is what one (scenario, target) evaluation resolved to on a cycle.
// Four values, never two: the whole point is that "we saw nothing wrong" and
// "we could not see" are different answers, and a tool that collapses them
// into silence cannot be trusted with either.
type Outcome string

const (
	// OutcomeFiring: series returned, threshold breached, held for `for`.
	OutcomeFiring Outcome = "firing"
	// OutcomeClear: series returned, below threshold. A real answer.
	OutcomeClear Outcome = "clear"
	// OutcomeBlind: the query returned no series or errored, or the target
	// failed its coverage probe. Not an answer; an admission.
	OutcomeBlind Outcome = "blind"
	// OutcomeUnlocked: the entry's hash is absent from catalog.lock, so it
	// is not the entry that was reviewed and does not run.
	OutcomeUnlocked Outcome = "unlocked"
	// OutcomeNotApplicable: the scenario's signal needs a template parameter
	// this target does not define, so the scenario was never a question for
	// it. Deliberately not one of the four reported states and not counted
	// as coverage either way: an ingress entry is not "blind" on a plain
	// service target, it is simply about something else.
	OutcomeNotApplicable Outcome = "not-applicable"
)

// DefaultCoverageProbe asserts that the target's namespace produces mesh
// request telemetry at all. It is namespace-scoped rather than service-scoped
// on purpose: a service-scoped probe would fail for exactly the incident
// traffic-vanished-triage exists to catch, and would turn a real outage into
// a shrug. Override per target with `coverageProbe` when a namespace is not
// the right unit of visibility.
const DefaultCoverageProbe = `count(istio_requests_total{destination_service_namespace="{{.namespace}}"})`

// Evaluation is one (scenario, target) pair's outcome on one cycle.
type Evaluation struct {
	Scenario string
	Params   map[string]string
	Outcome  Outcome
	Value    float64
	// Reason explains a blind or unlocked outcome in the words an operator
	// needs: which query returned nothing, which probe failed, which hash
	// did not match. Empty for firing and clear.
	Reason string
}

// Cycle is one full pass over every target, summarized. The detector hands it
// to OnCycle so the CLI can print the coverage line and a caller can assert on
// it without scraping logs.
type Cycle struct {
	Started    time.Time
	Observed   int // targets whose coverage probe returned series
	Unobserved int // targets whose coverage probe did not

	// InBreach counts evaluations whose signal is over its threshold on this
	// tick. It was called Firing, and the name was the bug: in Prometheus's
	// vocabulary an alert fires once it has held for its `for` duration,
	// while this counts the moment the comparison turns true, which is
	// earlier and sometimes much earlier. Two runs were misread because of
	// it, one showing two firing with a single report and one showing one
	// firing with no report at all. Both were the tool behaving correctly and
	// the summary describing it wrongly.
	//
	// A breach is always in exactly one of the five buckets below, and the
	// distinctions are not pedantic: they are five different reasons an
	// operator might be looking at silence.
	InBreach int
	// Reported: the hold was met, nothing suppressed it, and the handler took
	// the incident. This is the only one that produced a document.
	Reported int
	// Suppressed: held back as a cascade symptom of another incident that is
	// also breaching. A real breach, deliberately not a second report.
	Suppressed int
	// Waiting: in breach, but not yet for its `for` duration. Reporting
	// nothing is correct here, and it is indistinguishable from a healthy
	// cluster unless the number is shown.
	Waiting int
	// RateLimited: the hold was met and maxAppliesPerHour stopped a further
	// proposal. The signal is still breaching and the operator still needs to
	// know; a guardrail that hides what it blocked is its own fail-open.
	RateLimited int
	// Retrying: the handler errored, so the episode was kept rather than
	// marked delivered. It will be due again on the next tick.
	Retrying int

	Clear       int
	Blind       int
	Unlocked    int
	Evaluations []Evaluation
	// UnobservedTargets carries the params of each target that failed its
	// probe, so the operator is told which one rather than a count.
	UnobservedTargets []map[string]string
}

// Line renders the per-cycle coverage summary. It is printed every cycle on
// purpose, including when everything is fine: a coverage number that only
// appears when it is bad is a number nobody learns to read.
func (c Cycle) Line() string {
	breach := fmt.Sprintf("%d in breach", c.InBreach)
	// Break the number down only when there is something to break down, and
	// name only the non-zero parts. A line that always carries five numbers
	// is a line nobody reads, and the whole point of this change is that the
	// single number was being read wrongly.
	if c.InBreach > 0 {
		var parts []string
		for _, p := range []struct {
			n     int
			label string
		}{
			{c.Reported, "reported"},
			{c.Suppressed, "suppressed"},
			{c.Waiting, "awaiting hold"},
			{c.RateLimited, "rate-limited"},
			{c.Retrying, "handler retrying"},
		} {
			if p.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", p.n, p.label))
			}
		}
		if len(parts) > 0 {
			breach += " (" + strings.Join(parts, ", ") + ")"
		}
	}
	return fmt.Sprintf("%d targets observed, %d unobserved, %d scenarios blind (%s, %d clear, %d unlocked)",
		c.Observed, c.Unobserved, c.Blind, breach, c.Clear, c.Unlocked)
}

// Healthy reports whether the cycle proved the tool was looking at every
// target it was asked to watch. `check --once` exits non-zero when this is
// false: an unobserved target means the silence is not an answer.
func (c Cycle) Healthy() bool { return c.Unobserved == 0 }

// persistedEntry is one scenario/target lifecycle state on disk.
type persistedEntry struct {
	State state     `json:"state"`
	Since time.Time `json:"since"`
	// Applies carries the guardrail's rolling window across restarts, so
	// bouncing the process cannot be used to get around maxAppliesPerHour.
	Applies []time.Time `json:"applies,omitempty"`
}

// SaveState writes the detector's incident lifecycle to disk, atomically.
//
// Without this a restart re-opens every incident that is still breaching:
// the state machine starts empty, the breach walks inactive -> pending ->
// firing again, and in GitOps mode that is a duplicate pull request for an
// incident already under review. The learned baseline was already persisted;
// the incident state was not, which made a pod restart a correctness event.
func (d *Detector) SaveState(path string) error {
	if path == "" {
		return nil
	}
	snapshot := make(map[string]persistedEntry, len(d.states))
	for key, e := range d.states {
		// Only a live episode is worth carrying across a restart. An
		// inactive entry is the default on load, so writing it would grow
		// the file forever for no gain.
		if e.state == inactive && len(e.applies) == 0 {
			continue
		}
		snapshot[key] = persistedEntry{State: e.state, Since: e.since, Applies: e.applies}
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("state: rename %s: %w", path, err)
	}
	return nil
}

// LoadState restores the incident lifecycle from disk. A missing file is not
// an error: a first run has no open incidents. A corrupt file is an error the
// caller logs and continues past, because losing incident continuity is worse
// than refusing to start but not worse than not starting at all.
func (d *Detector) LoadState(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot map[string]persistedEntry
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("state: %s: %w", path, err)
	}
	for key, p := range snapshot {
		d.states[key] = &entry{state: p.State, since: p.Since, applies: p.Applies}
	}
	return nil
}
