package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

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
	if s.Remediation.Target.Kind == "" {
		return fmt.Errorf("remediation.target.kind is required")
	}
	if s.Remediation.PatchTemplate == "" {
		return fmt.Errorf("remediation.patchTemplate is required")
	}
	if _, err := template.New(s.ID).Option("missingkey=error").Parse(s.Remediation.PatchTemplate); err != nil {
		return fmt.Errorf("patchTemplate does not parse: %w", err)
	}
	return nil
}
