package catalog

// Scenario is one entry in the remediation catalog: a mesh failure mode the
// detector can recognize and the patch that answers it. Catalog entries are
// reviewed by humans; the engine never invents a remediation at runtime.
type Scenario struct {
	ID              string         `yaml:"id"`
	Title           string         `yaml:"title"`
	Severity        string         `yaml:"severity"`
	Description     string         `yaml:"description"`
	Signal          Signal         `yaml:"signal"`
	Evidence        []Query        `yaml:"evidence"`
	ObjectEvidence  []ObjectQuery  `yaml:"objectEvidence"`
	LogEvidence     []LogQuery     `yaml:"logEvidence"`
	RolloutEvidence []RolloutQuery `yaml:"rolloutEvidence"`
	Remediation     Remediation    `yaml:"remediation"`
	Guardrails      Guardrails     `yaml:"guardrails"`
	Rollback        string         `yaml:"rollback"`

	// Suppresses names scenarios that are symptoms of this one: while this
	// scenario is in breach for a target, the listed scenarios stay quiet
	// there. Overflow 503s inflating the 5xx ratio is one incident, not two.
	Suppresses []string `yaml:"suppresses"`
}

// Signal is the PromQL condition that fires the scenario. The query may use
// the same template parameters as the patch (service, namespace, workload,
// subset, stable_subset); they are substituted at evaluation time.
type Signal struct {
	PromQL     string  `yaml:"promql"`
	Comparison string  `yaml:"comparison"` // one of: > < >= <=
	Threshold  float64 `yaml:"threshold"`
	For        string  `yaml:"for"` // how long the condition must hold, e.g. "90s"

	// BaselineMultiplier, when > 0, makes the threshold relative to the
	// signal's learned baseline for this target: the effective threshold is
	// baseline * BaselineMultiplier once the baseline is trusted. Until then
	// the static Threshold applies, so warm-up never fires on noise. This is
	// how a scenario says "fire when this is 3x its own normal" instead of
	// picking a fixed number that is wrong for every cluster but one.
	BaselineMultiplier float64 `yaml:"baselineMultiplier"`
	// BaselineMinSamples is how many healthy observations must accumulate
	// before the relative threshold is trusted. Defaults to 20.
	BaselineMinSamples int `yaml:"baselineMinSamples"`
}

// Query is a named PromQL query whose result is attached to the pull request
// as evidence for the diagnosis.
type Query struct {
	Name   string `yaml:"name"`
	PromQL string `yaml:"promql"`
}

// ObjectQuery reads fields off one live Kubernetes object when a scenario
// fires, so the report can put the config-level cause (a bad env var, a
// wrong policy mode) next to the metric symptom. Object and namespace accept
// the same template parameters as the signal. Reads go through kubectl and
// are the only cluster access the engine has; it never writes.
type ObjectQuery struct {
	Name       string   `yaml:"name"`
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Object     string   `yaml:"object"`    // object name, template
	Namespace  string   `yaml:"namespace"` // template; empty means cluster-scoped
	Fields     []string `yaml:"fields"`    // dotted paths, e.g. spec.template.spec.containers[*].env
}

// LogQuery sweeps recent logs of every deployment in a namespace for known
// failure patterns when a scenario fires. It is the deterministic stand-in
// for "go read the client's logs": the patterns are curated, reviewable
// signatures (resolver failures, refused connections, TLS errors), and only
// matching lines reach the report.
type LogQuery struct {
	Name         string   `yaml:"name"`
	Namespace    string   `yaml:"namespace"` // template
	Patterns     []string `yaml:"patterns"`  // regexes, ORed
	SinceSeconds int      `yaml:"sinceSeconds"`
	MaxLines     int      `yaml:"maxLines"` // cap on matched lines per deployment
}

// RolloutQuery attaches recent deployment rollouts in a namespace, each with
// the line diff between its previous and current pod template. A bad deploy's
// root cause is usually a line in that diff.
type RolloutQuery struct {
	Name          string `yaml:"name"`
	Namespace     string `yaml:"namespace"` // template
	WithinMinutes int    `yaml:"withinMinutes"`
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
