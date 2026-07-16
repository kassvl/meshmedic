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
			fmt.Fprintf(&b, "| %s | %.4g |\n", e.Name, e.Value)
		}
	}

	fmt.Fprintf(&b, "\n### Proposed patch (%s)\n\n```yaml\n%s```\n", s.Remediation.Target.Kind, patch)
	fmt.Fprintf(&b, "\n### Rollback\n\n%s\n", strings.TrimSpace(s.Rollback))

	if s.Guardrails.RequiresApproval {
		fmt.Fprintf(&b, "\nThis scenario requires human approval; nothing was applied to the cluster.\n")
	}
	return b.String()
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
