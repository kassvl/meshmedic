package main

import (
	"reflect"
	"testing"

	"github.com/kassvl/meshmedic/pkg/proof"
)

func specs(ids ...string) []proof.Spec {
	out := make([]proof.Spec, 0, len(ids))
	for _, id := range ids {
		out = append(out, proof.Spec{Entry: id})
	}
	return out
}

func entries(sp []proof.Spec) []string {
	out := make([]string, 0, len(sp))
	for _, s := range sp {
		out = append(out, s.Entry)
	}
	return out
}

func TestSelectSpecs(t *testing.T) {
	all := specs("authz-deny-flood", "canary-latency-rollback", "error-surge", "no-route")

	cases := []struct {
		name        string
		want        []string
		wantEntries []string
		wantMissing []string
	}{
		{
			name:        "one entry, the form v1.0.0 shipped with",
			want:        []string{"error-surge"},
			wantEntries: []string{"error-surge"},
		},
		{
			name:        "two entries in one run, which is the point of the flag",
			want:        []string{"error-surge", "authz-deny-flood"},
			wantEntries: []string{"authz-deny-flood", "error-surge"},
		},
		{
			// Order comes from the spec directory, not from the command line,
			// so two people typing the same two entries in a different order
			// get the same run.
			name:        "selection order follows the specs, not the request",
			want:        []string{"no-route", "authz-deny-flood"},
			wantEntries: []string{"authz-deny-flood", "no-route"},
		},
		{
			name:        "a typo is named rather than silently dropped",
			want:        []string{"error-surge", "erorr-surge"},
			wantEntries: []string{"error-surge"},
			wantMissing: []string{"erorr-surge"},
		},
		{
			// The failure this reports on is a suite that ran less than it was
			// asked to. Naming one of two typos would still hide half of it.
			name:        "every typo is named, not just the first",
			want:        []string{"zzz-typo", "aaa-typo", "error-surge"},
			wantEntries: []string{"error-surge"},
			wantMissing: []string{"aaa-typo", "zzz-typo"},
		},
		{
			name:        "asking twice for the same entry runs it once",
			want:        []string{"error-surge", "error-surge"},
			wantEntries: []string{"error-surge"},
		},
		{
			name:        "nothing matches",
			want:        []string{"not-a-thing"},
			wantMissing: []string{"not-a-thing"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, missing := selectSpecs(all, tc.want)
			if gotIDs := entries(got); !reflect.DeepEqual(gotIDs, tc.wantEntries) &&
				!(len(gotIDs) == 0 && len(tc.wantEntries) == 0) {
				t.Errorf("selected %v, want %v", gotIDs, tc.wantEntries)
			}
			if !reflect.DeepEqual(missing, tc.wantMissing) &&
				!(len(missing) == 0 && len(tc.wantMissing) == 0) {
				t.Errorf("missing %v, want %v", missing, tc.wantMissing)
			}
		})
	}
}

// multiFlag is what makes --entry repeatable. Set must grow the slice it is
// called on, which is why its receiver is a pointer, and String must survive
// the zero value because the flag package builds one by reflection to decide
// whether to print a default.
func TestMultiFlagCollectsAndSurvivesItsZeroValue(t *testing.T) {
	var m multiFlag
	if got := m.String(); got != "" {
		t.Errorf("zero value stringified to %q, want empty", got)
	}
	for _, v := range []string{"a", "b", "c"} {
		if err := m.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if !reflect.DeepEqual([]string(m), []string{"a", "b", "c"}) {
		t.Errorf("collected %v, want [a b c]", m)
	}
	if got := m.String(); got != "a,b,c" {
		t.Errorf("String() = %q, want a,b,c", got)
	}
}
