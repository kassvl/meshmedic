// Package remediate turns a matched catalog scenario into a concrete patch.
package remediate

import (
	"bytes"
	"fmt"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

// Render fills a scenario's patch template with incident parameters and
// verifies the result parses as YAML, so a broken template can never reach
// a pull request. Missing parameters are an error, not an empty string.
func Render(s catalog.Scenario, params map[string]string) (string, error) {
	tmpl, err := template.New(s.ID).Option("missingkey=error").Parse(s.Remediation.PatchTemplate)
	if err != nil {
		return "", fmt.Errorf("scenario %s: %w", s.ID, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("scenario %s: %w", s.ID, err)
	}
	var probe any
	if err := yaml.Unmarshal(buf.Bytes(), &probe); err != nil {
		return "", fmt.Errorf("scenario %s: rendered patch is not valid YAML: %w", s.ID, err)
	}
	return buf.String(), nil
}
