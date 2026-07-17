// Package report renders an incident into the markdown document that
// becomes the pull request body. The CLI prints the same document, so what
// an operator sees in the terminal is exactly what reviewers will read.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/prom"
)

// Markdown renders one incident and its proposed patch.
func Markdown(inc detect.Incident, patch string) string {
	var b strings.Builder
	s := inc.Scenario

	fmt.Fprintf(&b, "## Incident: %s\n\n", s.Title)
	fmt.Fprintf(&b, "Scenario `%s` (severity %s) fired for %s.\n\n", s.ID, s.Severity, formatParams(inc.Params))
	fmt.Fprintf(&b, "The signal has held at **%.4g** (threshold %s %g for %s) since %s.\n\n",
		inc.Value, s.Signal.Comparison, s.Signal.Threshold, s.Signal.For,
		inc.Since.UTC().Format(time.RFC3339))

	fmt.Fprintf(&b, "### Diagnosis\n\n%s\n", strings.TrimSpace(s.Description))

	if len(inc.Evidence) > 0 {
		fmt.Fprintf(&b, "\n### Evidence\n\n| query | value |\n| --- | --- |\n")
		for _, e := range inc.Evidence {
			if e.Err != nil {
				fmt.Fprintf(&b, "| %s | unavailable (%v) |\n", e.Name, e.Err)
				continue
			}
			for _, s := range rankedSamples(e.Samples) {
				fmt.Fprintf(&b, "| %s | %.4g |\n", sampleName(e.Name, s.Labels), s.Value)
			}
		}
	}

	if len(inc.ObjectEvidence) > 0 {
		fmt.Fprintf(&b, "\n### Configuration evidence\n\n")
		for _, o := range inc.ObjectEvidence {
			if o.Err != nil {
				fmt.Fprintf(&b, "- %s (%s): unavailable (%v)\n", o.Name, o.Ref, o.Err)
				continue
			}
			paths := make([]string, 0, len(o.Fields))
			for p := range o.Fields {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				fmt.Fprintf(&b, "- %s (`%s`) `%s`: `%s`\n", o.Name, o.Ref, p, o.Fields[p])
			}
		}
	}

	if len(inc.LogEvidence) > 0 {
		fmt.Fprintf(&b, "\n### Log evidence\n\n")
		for _, le := range inc.LogEvidence {
			if le.Err != nil {
				fmt.Fprintf(&b, "- %s: unavailable (%v)\n", le.Name, le.Err)
				continue
			}
			deps := make([]string, 0, len(le.Matches))
			for dep := range le.Matches {
				deps = append(deps, dep)
			}
			sort.Strings(deps)
			hit := false
			for _, dep := range deps {
				if len(le.Matches[dep]) == 0 {
					continue
				}
				hit = true
				fmt.Fprintf(&b, "**%s** — `%s`:\n\n```\n", le.Name, dep)
				for _, line := range le.Matches[dep] {
					fmt.Fprintf(&b, "%s\n", line)
				}
				fmt.Fprintf(&b, "```\n")
			}
			if !hit {
				fmt.Fprintf(&b, "- %s: no matching log lines in the namespace\n", le.Name)
			}
		}
	}

	if len(inc.RolloutEvidence) > 0 {
		fmt.Fprintf(&b, "\n### Recent rollouts\n\n")
		for _, re := range inc.RolloutEvidence {
			if re.Err != nil {
				fmt.Fprintf(&b, "- %s: unavailable (%v)\n", re.Name, re.Err)
				continue
			}
			if len(re.Rollouts) == 0 {
				fmt.Fprintf(&b, "- %s: no rollouts in the window\n", re.Name)
				continue
			}
			for _, r := range re.Rollouts {
				fmt.Fprintf(&b, "**%s** — `%s` rolled %ds ago, template diff (previous → current):\n\n```diff\n%s\n```\n",
					re.Name, r.Deployment, r.AgeSeconds, r.Diff)
			}
		}
	}

	fmt.Fprintf(&b, "\n### Proposed patch (%s)\n\n```yaml\n%s```\n", s.Remediation.Target.Kind, patch)
	fmt.Fprintf(&b, "\n### Rollback\n\n%s\n", strings.TrimSpace(s.Rollback))

	if s.Guardrails.RequiresApproval {
		fmt.Fprintf(&b, "\nThis scenario requires human approval; nothing was applied to the cluster.\n")
	}
	return b.String()
}

// sampleName renders one evidence row's identity: the query name plus the
// sample's labels in PromQL selector form, so a per-workload breakdown reads
// like errors-by-workload{destination_workload="payments-v2"}.
func sampleName(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	return name + "{" + strings.Join(parts, ", ") + "}"
}

// rankedSamples orders a breakdown biggest value first, so the offending
// workload is the first row a reviewer reads. Ties break on labels to keep
// the report deterministic.
func rankedSamples(samples []prom.Sample) []prom.Sample {
	out := make([]prom.Sample, len(samples))
	copy(out, samples)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return sampleName("", out[i].Labels) < sampleName("", out[j].Labels)
	})
	return out
}

func formatParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("`%s=%s`", k, params[k]))
	}
	return strings.Join(parts, " ")
}
