package detect

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"gopkg.in/yaml.v3"
)

// Config is the watch configuration: where Prometheus lives, how often to
// evaluate, and which targets to watch.
type Config struct {
	Prometheus string   `yaml:"prometheus"`
	Interval   string   `yaml:"interval"`
	Targets    []Target `yaml:"targets"`
	GitOps     *GitOps  `yaml:"gitops"`
	// BaselineState is where the learned per-signal baseline is persisted.
	// Empty disables baseline-relative thresholds; scenarios then use their
	// static thresholds only.
	BaselineState string `yaml:"baselineState"`

	// AnomalyWatch and UnmatchedLog drive the unmatched-incident recorder:
	// generic signals baselined per target, with a fingerprint appended to
	// UnmatchedLog when one deviates while no catalog scenario is active.
	// Both must be set, and BaselineState too, for the recorder to run.
	AnomalyWatch []catalog.Query `yaml:"anomalyWatch"`
	UnmatchedLog string          `yaml:"unmatchedLog"`
}

// GitOps configures where remediation pull requests go. Absent means watch
// only prints incident reports.
type GitOps struct {
	Repo string `yaml:"repo"` // owner/repo of the config repository
	Base string `yaml:"base"` // base branch; empty means the repo default
	Path string `yaml:"path"` // patch file path template inside the repo
}

const defaultInterval = 30 * time.Second

// LoadConfig reads and validates a watch config file.
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Prometheus == "" {
		return c, fmt.Errorf("%s: prometheus URL is required", path)
	}
	if len(c.Targets) == 0 {
		return c, fmt.Errorf("%s: at least one target is required", path)
	}
	for i, t := range c.Targets {
		if len(t.Params) == 0 {
			return c, fmt.Errorf("%s: target %d has no params", path, i)
		}
	}
	if c.Interval != "" {
		if _, err := time.ParseDuration(c.Interval); err != nil {
			return c, fmt.Errorf("%s: interval: %w", path, err)
		}
	}
	if c.GitOps != nil {
		if !strings.Contains(c.GitOps.Repo, "/") {
			return c, fmt.Errorf("%s: gitops.repo must be owner/repo, got %q", path, c.GitOps.Repo)
		}
		if c.GitOps.Path == "" {
			c.GitOps.Path = "meshmedic/{{.namespace}}/{{.scenario}}.yaml"
		}
	}
	return c, nil
}

// ConfigFromFlags builds a watch config without a file, from a Prometheus URL
// and one or more `key=value,key=value` target strings. It exists so that
// pointing MeshMedic at a Prometheus you already run takes one command and no
// YAML: the difference between reading the README and running the tool.
//
// Only the report-printing path is reachable this way. Opening pull requests
// needs a config file, deliberately: writing to a repository is not something
// to enable from a flag someone pasted out of a README.
func ConfigFromFlags(prometheus, interval string, targets []string) (Config, error) {
	var c Config
	if prometheus == "" {
		return c, fmt.Errorf("--prometheus is required")
	}
	if len(targets) == 0 {
		return c, fmt.Errorf("at least one --target is required, e.g. --target service=payments,namespace=demo")
	}
	c.Prometheus = prometheus
	c.Interval = interval
	if interval != "" {
		if _, err := time.ParseDuration(interval); err != nil {
			return c, fmt.Errorf("--interval: %w", err)
		}
	}
	for _, spec := range targets {
		params, err := ParseTargetSpec(spec)
		if err != nil {
			return c, err
		}
		c.Targets = append(c.Targets, Target{Params: params})
	}
	return c, nil
}

// ParseTargetSpec turns `service=payments,namespace=demo` into template
// parameters. An empty key, a missing `=`, or a duplicate key is an error
// rather than a silent surprise during an incident.
func ParseTargetSpec(spec string) (map[string]string, error) {
	params := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("bad --target %q: want key=value pairs separated by commas", spec)
		}
		if _, dup := params[k]; dup {
			return nil, fmt.Errorf("bad --target %q: key %q appears twice", spec, k)
		}
		params[k] = strings.TrimSpace(v)
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("bad --target %q: no key=value pairs", spec)
	}
	return params, nil
}

// IntervalDuration returns the evaluation interval, defaulting to 30s.
func (c Config) IntervalDuration() time.Duration {
	if c.Interval == "" {
		return defaultInterval
	}
	// Parse errors are impossible here: LoadConfig rejects them.
	d, _ := time.ParseDuration(c.Interval)
	return d
}
