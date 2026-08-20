package proof

import (
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

func holdScenario(id string, hold time.Duration) catalog.Scenario {
	return catalog.Scenario{ID: id, Signal: catalog.Signal{For: hold.String()}}
}

func deadlineSpec(entry string, firesWithin time.Duration) Spec {
	return Spec{Entry: entry, FiresWithin: Duration(firesWithin)}
}

func TestCheckWinnable(t *testing.T) {
	scenarios := []catalog.Scenario{
		holdScenario("inside-the-hold", 5*time.Minute),
		holdScenario("exactly-the-hold", 5*time.Minute),
		holdScenario("thin", time.Minute),
		holdScenario("comfortable", time.Minute),
		holdScenario("no-hold", 0),
	}
	specs := []Spec{
		deadlineSpec("inside-the-hold", 4*time.Minute),
		deadlineSpec("exactly-the-hold", 5*time.Minute),
		deadlineSpec("thin", 90*time.Second),
		deadlineSpec("comfortable", 6*time.Minute),
		deadlineSpec("no-hold", 30*time.Second),
	}

	got := map[string]Winnability{}
	for _, w := range CheckWinnable(scenarios, specs) {
		got[w.Entry] = w
	}

	if !got["inside-the-hold"].Unwinnable() {
		t.Error("a 4m deadline on a 5m hold must be unwinnable")
	}
	// The boundary is the interesting one: a deadline equal to the hold gives
	// the detector no tick in which to deliver, so equality is not enough.
	if !got["exactly-the-hold"].Unwinnable() {
		t.Error("a deadline exactly equal to the hold leaves no tick to deliver in and must be unwinnable")
	}
	if got["thin"].Unwinnable() {
		t.Error("a 90s deadline on a 60s hold is winnable, if barely")
	}
	if !got["thin"].Thin() {
		t.Error("30s of slack should be reported as thin")
	}
	if got["comfortable"].Thin() || got["comfortable"].Unwinnable() {
		t.Errorf("5m of slack is neither thin nor unwinnable, got %v", got["comfortable"])
	}
	// An entry with no hold reports on the first breaching tick, so any
	// positive deadline is winnable.
	if got["no-hold"].Unwinnable() {
		t.Error("an entry with no hold duration can be proven inside any positive deadline")
	}
}

// The message has to send the reader to the arithmetic rather than to the
// mesh, because the symptom of an unwinnable proof is "never fired", which
// looks exactly like a broken entry.
func TestUnwinnableExplainsItselfAsArithmetic(t *testing.T) {
	w := CheckWinnable(
		[]catalog.Scenario{holdScenario("e", 5*time.Minute)},
		[]Spec{deadlineSpec("e", 3*time.Minute)},
	)[0]
	msg := w.String()
	for _, want := range []string{"firesWithin", "hold", "no correct entry can meet it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should mention %q, got: %s", want, msg)
		}
	}
}

// Tightest first: a list read from the top should start at the edge.
func TestCheckWinnableOrdersTightestFirst(t *testing.T) {
	scenarios := []catalog.Scenario{
		holdScenario("roomy", time.Minute),
		holdScenario("tight", time.Minute),
		holdScenario("broken", time.Minute),
	}
	specs := []Spec{
		deadlineSpec("roomy", 10*time.Minute),
		deadlineSpec("tight", 70*time.Second),
		deadlineSpec("broken", 30*time.Second),
	}
	got := CheckWinnable(scenarios, specs)
	if got[0].Entry != "broken" || got[1].Entry != "tight" || got[2].Entry != "roomy" {
		t.Errorf("order = %s, %s, %s; want broken, tight, roomy",
			got[0].Entry, got[1].Entry, got[2].Entry)
	}
}

// A spec whose entry is not in the catalog is already an error with a better
// message elsewhere. Reporting it here too would send someone to the deadline
// when the problem is the id.
func TestCheckWinnableIgnoresUnknownEntries(t *testing.T) {
	got := CheckWinnable(
		[]catalog.Scenario{holdScenario("real", time.Minute)},
		[]Spec{deadlineSpec("real", 5*time.Minute), deadlineSpec("ghost", time.Second)},
	)
	if len(got) != 1 || got[0].Entry != "real" {
		t.Errorf("got %d results, want only the known entry", len(got))
	}
}

// The repository's own specs must satisfy the rule the Spec documentation has
// always stated. This is the test that turns a comment into an invariant.
func TestShippedSpecsAreWinnable(t *testing.T) {
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	specs, err := LoadDir("../../proof")
	if err != nil {
		t.Fatalf("proofs: %v", err)
	}
	for _, w := range CheckWinnable(scenarios, specs) {
		if w.Unwinnable() {
			t.Errorf("shipped spec cannot be won: %s", w)
		}
		if w.Thin() {
			t.Errorf("shipped spec is too tight to be reliable: %s", w)
		}
	}
}
