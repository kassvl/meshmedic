package catalog

// Scenario is one entry in the remediation catalog: a mesh failure mode the
// detector can recognize and the patch that answers it. Catalog entries are
// reviewed by humans; the engine never invents a remediation at runtime.
type Scenario struct {
	ID          string      `yaml:"id"`
	Title       string      `yaml:"title"`
	Severity    string      `yaml:"severity"`
	Description string      `yaml:"description"`
	Signal      Signal      `yaml:"signal"`
	Evidence    []Query     `yaml:"evidence"`
	Remediation Remediation `yaml:"remediation"`
	Guardrails  Guardrails  `yaml:"guardrails"`
	Rollback    string      `yaml:"rollback"`
}

// Signal is the PromQL condition that fires the scenario. The query may use
// the same template parameters as the patch (service, namespace, workload,
// subset, stable_subset); they are substituted at evaluation time.
type Signal struct {
	PromQL     string  `yaml:"promql"`
	Comparison string  `yaml:"comparison"` // one of: > < >= <=
	Threshold  float64 `yaml:"threshold"`
	For        string  `yaml:"for"` // how long the condition must hold, e.g. "90s"
}

// Query is a named PromQL query whose result is attached to the pull request
// as evidence for the diagnosis.
type Query struct {
	Name   string `yaml:"name"`
	PromQL string `yaml:"promql"`
}

// Remediation describes the patch a scenario renders when it fires.
type Remediation struct {
	Target        Target `yaml:"target"`
	Action        string `yaml:"action"`
	PatchTemplate string `yaml:"patchTemplate"`
}

// Target identifies the Kubernetes resource kind the patch applies to.
type Target struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
}

// Guardrails bound what automation may do without a human in the loop.
type Guardrails struct {
	RequiresApproval  bool `yaml:"requiresApproval"`
	MaxAppliesPerHour int  `yaml:"maxAppliesPerHour"`
}
