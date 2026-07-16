package detect

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the watch configuration: where Prometheus lives, how often to
// evaluate, and which targets to watch.
type Config struct {
	Prometheus string   `yaml:"prometheus"`
	Interval   string   `yaml:"interval"`
	Targets    []Target `yaml:"targets"`
	GitOps     *GitOps  `yaml:"gitops"`
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

// IntervalDuration returns the evaluation interval, defaulting to 30s.
func (c Config) IntervalDuration() time.Duration {
	if c.Interval == "" {
		return defaultInterval
	}
	// Parse errors are impossible here: LoadConfig rejects them.
	d, _ := time.ParseDuration(c.Interval)
	return d
}
