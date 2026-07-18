package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

var validComparisons = map[string]bool{">": true, "<": true, ">=": true, "<=": true}

// LoadDir reads every *.yaml file in dir as a Scenario, validates each one,
// and rejects duplicate IDs. Scenarios are returned sorted by ID so output
// is stable.
func LoadDir(dir string) ([]Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading catalog dir: %w", err)
	}

	var scenarios []Scenario
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		var s Scenario
		if err := yaml.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if prev, dup := seen[s.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate scenario id %q (also in %s)", e.Name(), s.ID, prev)
		}
		seen[s.ID] = e.Name()
		scenarios = append(scenarios, s)
	}

	sort.Slice(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })

	// Suppression references are cross-scenario, so they validate against
	// the loaded set: unknown ids and mutual suppression are authoring
	// errors that would otherwise silence real incidents at runtime.
	suppresses := map[string]map[string]bool{}
	for _, s := range scenarios {
		set := map[string]bool{}
		for _, id := range s.Suppresses {
			if id == s.ID {
				return nil, fmt.Errorf("%s: scenario %q suppresses itself", seen[s.ID], s.ID)
			}
			if _, ok := seen[id]; !ok {
				return nil, fmt.Errorf("%s: scenario %q suppresses unknown scenario %q", seen[s.ID], s.ID, id)
			}
			set[id] = true
		}
		suppresses[s.ID] = set
	}
	for _, s := range scenarios {
		for id := range suppresses[s.ID] {
			if suppresses[id][s.ID] {
				return nil, fmt.Errorf("scenarios %q and %q suppress each other; both would stay silent", s.ID, id)
			}
		}
	}
	return scenarios, nil
}

// Validate checks the fields the engine depends on. A scenario that fails
// validation never loads, so a broken catalog is caught at startup and in CI,
// not during an incident.
func (s Scenario) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("id is required")
	}
	if s.Title == "" {
		return fmt.Errorf("title is required")
	}
	if s.Signal.PromQL == "" {
		return fmt.Errorf("signal.promql is required")
	}
	if !validComparisons[s.Signal.Comparison] {
		return fmt.Errorf("signal.comparison %q is not one of > < >= <=", s.Signal.Comparison)
	}
	if s.Signal.BaselineMultiplier < 0 {
		return fmt.Errorf("signal.baselineMultiplier must not be negative")
	}
	if s.Signal.For != "" {
		if _, err := time.ParseDuration(s.Signal.For); err != nil {
			return fmt.Errorf("signal.for: %w", err)
		}
	}
	for i, q := range s.ObjectEvidence {
		if q.Name == "" || q.APIVersion == "" || q.Kind == "" || q.Object == "" {
			return fmt.Errorf("objectEvidence[%d]: name, apiVersion, kind and object are all required", i)
		}
		if len(q.Fields) == 0 {
			return fmt.Errorf("objectEvidence[%d] (%s): at least one field is required", i, q.Name)
		}
	}
	for i, q := range s.LogEvidence {
		if q.Name == "" || q.Namespace == "" || len(q.Patterns) == 0 {
			return fmt.Errorf("logEvidence[%d]: name, namespace and at least one pattern are required", i)
		}
		for _, p := range q.Patterns {
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("logEvidence[%d] (%s): pattern %q: %w", i, q.Name, p, err)
			}
		}
	}
	for i, q := range s.RolloutEvidence {
		if q.Name == "" || q.Namespace == "" || q.WithinMinutes <= 0 {
			return fmt.Errorf("rolloutEvidence[%d]: name, namespace and withinMinutes are required", i)
		}
	}
	if s.Remediation.Target.Kind == "" {
		return fmt.Errorf("remediation.target.kind is required")
	}
	// Report-only scenarios produce a triage dossier, not a patch; every
	// other scenario must render one.
	if s.Remediation.Action != "report-only" {
		if s.Remediation.PatchTemplate == "" {
			return fmt.Errorf("remediation.patchTemplate is required")
		}
		if _, err := template.New(s.ID).Option("missingkey=error").Parse(s.Remediation.PatchTemplate); err != nil {
			return fmt.Errorf("patchTemplate does not parse: %w", err)
		}
	}
	return nil
}
