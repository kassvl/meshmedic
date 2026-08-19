// Package proof runs the end-to-end evidence for a catalog entry: inject the
// fault it claims to detect, watch a real detector against a real cluster, and
// assert that the entry fired, named the right culprit, kept its neighbours
// quiet, and cleared when the fault was removed.
//
// This is deliberately not part of the meshmedic binary. That binary's central
// claim is that it holds no cluster write credentials and mutates nothing, and
// a subcommand that runs `kubectl apply` would undermine the claim at the level
// of perception even though the detection path is untouched. The prover is a
// separate tool, built from the same packages, excluded from the release
// archives and the container image.
//
// What a proof establishes, and what it does not:
//
//   - It establishes that the entry fires on a real fault within its own hold
//     duration, that the report names the thing an operator needs to act on,
//     that the entries it claims to suppress stay quiet, that no unrelated
//     entry fires alongside it, and that it resolves on reset.
//   - It does not establish that the entry catches every instance of its
//     class, or that the fault injected here is the shape the failure takes in
//     production. Those are honesty limits, not defects, and the report says
//     so rather than implying coverage it has not measured.
package proof

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// unprovableFile holds the declarations of entries that cannot be proven on
// this testbed. It lives beside the proofs so the inventory and the exceptions
// are read from one place.
const unprovableFile = "UNPROVABLE.yaml"

// Spec is the declarative proof for one catalog entry. Keeping it declarative
// rather than a shell script is what makes adding the next entry's proof a
// reviewable diff instead of a new program.
type Spec struct {
	// Entry is the catalog scenario id this proof is about.
	Entry string `yaml:"entry"`
	// Summary is one line on what fault this injects and why that fault is
	// the honest representative of the entry's failure class.
	Summary string `yaml:"summary"`

	// Target is the set of template parameters to watch.
	Target map[string]string `yaml:"target"`

	// Inject and Reset are the commands that create and remove the fault.
	// Reset runs even when the proof fails, so a failed run does not leave a
	// broken testbed behind for the next one to inherit.
	//
	// Reset commands must be idempotent. Reset runs twice on a passing proof,
	// once to prove the incident closes and once in the deferred cleanup, so a
	// command that errors when its work is already done fails a proof whose
	// entry did everything right. Prefer `patch --type=merge` with a null over
	// a JSON `remove` op, and `delete --ignore-not-found` over a bare delete.
	Inject []Command `yaml:"inject"`
	Reset  []Command `yaml:"reset"`

	// Warmup, when set, ticks the detector against healthy traffic before the
	// fault is injected, so a relative-threshold entry has a learned baseline
	// to fire against. Without it such an entry falls back to its static
	// threshold, and for the latency entry that threshold is high enough that
	// reaching it also trips the canary entry, which suppresses the subject.
	Warmup Duration `yaml:"warmup"`
	// Settle is how long to wait after injecting before the clock on
	// FiresWithin starts, for rate windows to fill. Defaults to 0.
	Settle Duration `yaml:"settle"`
	// FiresWithin bounds how long the entry may take to fire once settled.
	// It must exceed the entry's hold duration or the proof is unwinnable.
	FiresWithin Duration `yaml:"firesWithin"`
	// ResolvesWithin bounds how long the entry may take to clear after reset.
	// Zero skips the resolution half of the proof, for entries whose fault
	// cannot be cleanly removed.
	ResolvesWithin Duration `yaml:"resolvesWithin"`

	Expect Expect `yaml:"expect"`
}

// Expect is what the proof asserts.
type Expect struct {
	// Names are strings the incident report must contain. This is the
	// difference between "an entry fired" and "the entry did its job": a
	// dependency incident that fires without naming the dependency has
	// detected something and explained nothing.
	Names []string `yaml:"names"`
	// Quiet lists entries that must not fire during the run. Entries the
	// subject suppresses belong here, and so does any entry whose firing
	// would mean the fault was not as isolated as the proof claims.
	Quiet []string `yaml:"quiet"`
	// AllowFiring lists entries that may fire alongside the subject without
	// failing the proof, for faults that genuinely produce two incidents.
	// Every id here needs a reason in the spec's summary.
	AllowFiring []string `yaml:"allowFiring"`
}

// Command is one step of injection or reset. Argv form, never a shell string:
// a proof that depends on shell quoting is a proof that breaks on someone
// else's machine for reasons unrelated to the entry.
type Command struct {
	Run  []string `yaml:"run"`
	Desc string   `yaml:"desc"`
}

func (c Command) String() string {
	if c.Desc != "" {
		return c.Desc
	}
	return strings.Join(c.Run, " ")
}

// Duration is a YAML-friendly time.Duration.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// LoadDir reads every *.yaml in dir as a Spec and validates it.
func LoadDir(dir string) ([]Spec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading proof dir: %w", err)
	}
	var specs []Spec
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		// UNPROVABLE.yaml is the declaration of entries that cannot be proven
		// here, not a proof. Skipped by name rather than by a clever rule,
		// because a clever rule is the kind of thing that silently swallows a
		// real spec someone named badly.
		if e.Name() == unprovableFile {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		var s Spec
		if err := yaml.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if prev, dup := seen[s.Entry]; dup {
			return nil, fmt.Errorf("%s: duplicate proof for entry %q (also in %s)", e.Name(), s.Entry, prev)
		}
		seen[s.Entry] = e.Name()
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Entry < specs[j].Entry })
	return specs, nil
}

// Validate rejects a spec that could not prove anything.
func (s Spec) Validate() error {
	if s.Entry == "" {
		return fmt.Errorf("entry is required")
	}
	if s.Summary == "" {
		return fmt.Errorf("%s: summary is required: a proof whose fault nobody explained is not reviewable", s.Entry)
	}
	if len(s.Target) == 0 {
		return fmt.Errorf("%s: target is required", s.Entry)
	}
	if len(s.Inject) == 0 {
		return fmt.Errorf("%s: at least one inject command is required", s.Entry)
	}
	if len(s.Reset) == 0 {
		return fmt.Errorf("%s: at least one reset command is required: a proof that leaves the fault behind poisons every run after it", s.Entry)
	}
	if s.FiresWithin.D() <= 0 {
		return fmt.Errorf("%s: firesWithin is required and must be positive", s.Entry)
	}
	if len(s.Expect.Names) == 0 {
		return fmt.Errorf("%s: expect.names is required: proving that something fired, without proving it named the culprit, proves the entry detected and explained nothing", s.Entry)
	}
	for i, c := range append(append([]Command{}, s.Inject...), s.Reset...) {
		if len(c.Run) == 0 {
			return fmt.Errorf("%s: command %d has no argv", s.Entry, i)
		}
	}
	// An entry cannot be both required to stay quiet and allowed to fire.
	allowed := map[string]bool{}
	for _, id := range s.Expect.AllowFiring {
		allowed[id] = true
	}
	for _, id := range s.Expect.Quiet {
		if allowed[id] {
			return fmt.Errorf("%s: %q is listed both as quiet and as allowed to fire", s.Entry, id)
		}
		if id == s.Entry {
			return fmt.Errorf("%s: the entry under proof cannot be required to stay quiet", s.Entry)
		}
	}
	return nil
}
