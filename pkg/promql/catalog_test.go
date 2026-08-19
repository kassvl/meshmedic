package promql

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

type catalogQuery struct {
	where string
	query string
}

// representativeParams stands in for a real target so the Go templates in the
// catalog render. The values only have to be plausible identifiers; the
// extractor cares about the shape of the query, not the target.
var representativeParams = map[string]string{
	"service": "payments", "namespace": "demo", "workload": "payments-v2",
	"subset": "v2", "stable_subset": "v1", "ingress_workload": "ingress-istio",
}

// catalogQueries renders every signal, evidence and anomaly query in the
// shipped catalog with representative parameters.
func catalogQueries(t *testing.T) []catalogQuery {
	t.Helper()
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	var out []catalogQuery
	render := func(where, tmpl string) {
		p, err := template.New(where).Option("missingkey=error").Parse(tmpl)
		if err != nil {
			t.Fatalf("%s: parsing template: %v", where, err)
		}
		var buf bytes.Buffer
		if err := p.Execute(&buf, representativeParams); err != nil {
			t.Fatalf("%s: rendering: %v (add the param to representativeParams)", where, err)
		}
		out = append(out, catalogQuery{where, strings.TrimSpace(buf.String())})
	}
	for _, s := range scenarios {
		render(s.ID+"/signal", s.Signal.PromQL)
		for _, e := range s.Evidence {
			render(s.ID+"/evidence/"+e.Name, e.PromQL)
		}
	}
	return out
}
