package proof

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

// Mutation testing, applied to the guards rather than to the code.
//
// Every guard in this repository claims to catch something. Nothing checks
// that the claims are true, and on 2026-08-20 four separate checks turned out
// to be checking nothing: a preflight that called a testbed quiet on the
// strength of a dead Prometheus, a summary that printed FAIL for a run nobody
// observed, a coverage count that measured a command-line filter, and an
// archive assertion in CI that failed on its own pipe before it read an
// archive. All four were green. None were checking.
//
// So: break the shipped catalog and specs on purpose, one property at a time,
// and assert the guard that owns that property rejects the result. A mutation
// that survives is a guard that is decorative, and finding that out costs
// milliseconds here rather than a cluster and fifteen minutes.
//
// These are the mutations a guard should catch without a cluster. The ones
// that need a live run, an inverted comparison or a threshold above the
// fault's own peak, are a separate and far more expensive exercise.

// shipped loads the real catalog and proof directories. Mutating copies of
// what actually ships is the point: a mutation suite over invented fixtures
// proves the fixtures are well formed.
func shipped(t *testing.T) ([]catalog.Scenario, []Spec) {
	t.Helper()
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	specs, err := LoadDir("../../proof")
	if err != nil {
		t.Fatalf("proofs: %v", err)
	}
	if len(scenarios) == 0 || len(specs) == 0 {
		t.Fatal("nothing shipped to mutate")
	}
	return scenarios, specs
}

// TestCatalogValidationCatchesItsMutations breaks one property of a real entry
// at a time and asserts Scenario.Validate rejects it.
func TestCatalogValidationCatchesItsMutations(t *testing.T) {
	scenarios, _ := shipped(t)

	mutations := []struct {
		name   string
		break_ func(*catalog.Scenario)
	}{
		{"id removed", func(s *catalog.Scenario) { s.ID = "" }},
		{"title removed", func(s *catalog.Scenario) { s.Title = "" }},
		{"signal query emptied", func(s *catalog.Scenario) { s.Signal.PromQL = "" }},
		{"comparison corrupted", func(s *catalog.Scenario) { s.Signal.Comparison = "=~" }},
		{"hold duration made unparseable", func(s *catalog.Scenario) { s.Signal.For = "two minutes" }},
		{"baseline multiplier made negative", func(s *catalog.Scenario) { s.Signal.BaselineMultiplier = -3 }},
		{"remediation kind removed", func(s *catalog.Scenario) { s.Remediation.Target.Kind = "" }},
	}

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			// Every shipped entry must reject this mutation, not just the
			// first one that happens to be in the directory.
			for _, orig := range scenarios {
				mutant := orig
				m.break_(&mutant)
				if err := mutant.Validate(); err == nil {
					t.Fatalf("%s survived %q: the validation that owns this property is not checking it",
						orig.ID, m.name)
				}
			}
		})
	}
}

// TestSpecValidationCatchesItsMutations does the same for proof specs.
func TestSpecValidationCatchesItsMutations(t *testing.T) {
	_, specs := shipped(t)

	mutations := []struct {
		name   string
		break_ func(*Spec)
	}{
		{"entry removed", func(s *Spec) { s.Entry = "" }},
		{"summary removed", func(s *Spec) { s.Summary = "" }},
		{"target removed", func(s *Spec) { s.Target = nil }},
		{"inject removed", func(s *Spec) { s.Inject = nil }},
		{"reset removed", func(s *Spec) { s.Reset = nil }},
		{"deadline removed", func(s *Spec) { s.FiresWithin = 0 }},
		// The one that matters most: a proof that asserts something fired,
		// without asserting it named the culprit, proves the entry detected
		// something and explained nothing.
		{"expected names removed", func(s *Spec) { s.Expect.Names = nil }},
		{"a command left with no argv", func(s *Spec) {
			s.Inject = append([]Command{{Desc: "empty"}}, s.Inject...)
		}},
	}

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			for _, orig := range specs {
				mutant := orig
				m.break_(&mutant)
				if err := mutant.Validate(); err == nil {
					t.Fatalf("%s survived %q: Spec.Validate is not checking it", orig.Entry, m.name)
				}
			}
		})
	}
}

// TestWinnabilityCatchesADeadlineMutation is the guard added after an entry's
// hold was raised and its proof's deadline had to be raised by hand in the
// same edit. Forgetting produces "never fired", which reads as a broken entry
// and is arithmetic.
func TestWinnabilityCatchesADeadlineMutation(t *testing.T) {
	scenarios, specs := shipped(t)

	for i := range scenarios {
		mutants := append([]catalog.Scenario(nil), scenarios...)
		// Push this entry's hold well past every shipped deadline.
		mutants[i].Signal.For = (24 * time.Hour).String()

		var caught bool
		for _, w := range CheckWinnable(mutants, specs) {
			if w.Entry == mutants[i].ID && w.Unwinnable() {
				caught = true
			}
		}
		// Only entries that actually have a proof can be caught this way;
		// the two declared unprovable have no spec to make unwinnable.
		hasSpec := false
		for _, sp := range specs {
			if sp.Entry == mutants[i].ID {
				hasSpec = true
			}
		}
		if hasSpec && !caught {
			t.Errorf("%s: a 24h hold against its deadline was not reported unwinnable", mutants[i].ID)
		}
	}
}

// TestEntryExistenceCatchesARenameMutation covers the cross-check between the
// two directories: a proof naming an entry that is not in the catalog proves
// nothing, and a typo in an id would otherwise silently skip the entry it
// meant to test.
func TestEntryExistenceCatchesARenameMutation(t *testing.T) {
	scenarios, specs := shipped(t)

	if missing := CheckEntriesExist(scenarios, specs); len(missing) != 0 {
		t.Fatalf("the shipped directories already disagree: %v", missing)
	}

	for i := range specs {
		mutants := append([]Spec(nil), specs...)
		original := mutants[i].Entry
		mutants[i].Entry = original + "-typo"

		missing := CheckEntriesExist(scenarios, mutants)
		if len(missing) != 1 || missing[0] != original+"-typo" {
			t.Errorf("renaming %s to %s-typo produced %v, want exactly that one id",
				original, original, missing)
		}
	}
}

// TestLockCatchesASemanticMutation is the difference between accepted and
// merely committed. The lock hashes the parsed entry, so reformatting is free
// and changing what the entry does is not.
func TestLockCatchesASemanticMutation(t *testing.T) {
	scenarios, _ := shipped(t)
	lock, err := catalog.LoadLock("../../catalog.lock")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	// Verify, not Stale. Stale answers a different question, which approvals
	// outlived their entry, and reaching for it here made the suite's first
	// run report sixteen surviving mutations that were all artefacts of the
	// wrong call. That is worth leaving in the record: a red check is no more
	// proof that its subject is broken than a green one is proof it works.
	for id, st := range mustVerify(t, lock, scenarios) {
		if st == catalog.StatusMismatch {
			t.Fatalf("%s is already adrift from its lock before any mutation", id)
		}
	}

	mutations := []struct {
		name   string
		break_ func(*catalog.Scenario)
	}{
		{"threshold raised", func(s *catalog.Scenario) { s.Signal.Threshold *= 10 }},
		{"comparison inverted", func(s *catalog.Scenario) { s.Signal.Comparison = "<" }},
		{"hold shortened", func(s *catalog.Scenario) { s.Signal.For = "1s" }},
		{"signal query rewritten", func(s *catalog.Scenario) { s.Signal.PromQL = "vector(1)" }},
		{"suppression removed", func(s *catalog.Scenario) { s.Suppresses = nil }},
	}

	locked := map[string]bool{}
	for id, st := range mustVerify(t, lock, scenarios) {
		locked[id] = st == catalog.StatusLocked
	}

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			for i := range scenarios {
				if !locked[scenarios[i].ID] {
					continue // an unlocked entry cannot drift from an approval it never had
				}
				mutants := append([]catalog.Scenario(nil), scenarios...)
				before := mutants[i]
				m.break_(&mutants[i])
				if mutants[i].Signal.Comparison == before.Signal.Comparison &&
					mutants[i].Signal.Threshold == before.Signal.Threshold &&
					mutants[i].Signal.For == before.Signal.For &&
					mutants[i].Signal.PromQL == before.Signal.PromQL &&
					len(mutants[i].Suppresses) == len(before.Suppresses) {
					continue // this mutation was a no-op for this entry
				}
				status := mustVerify(t, lock, mutants)
				if status[mutants[i].ID] != catalog.StatusMismatch {
					t.Errorf("%s survived %q: the lock still calls it %s, so what runs is not what was reviewed",
						mutants[i].ID, m.name, status[mutants[i].ID])
				}
			}
		})
	}
}

// The lock hashes the parsed entry, so a change that never reaches the parsed
// struct must not move it. Otherwise every reindentation forces a
// re-approval, people learn to re-approve without reading, and the lock stops
// meaning anything because a rubber stamp looks exactly like a guarantee.
//
// The first version of this test mutated Description and expected the hash to
// hold, which was wrong and worth recording. Description is not a comment: it
// is the Diagnosis paragraph the incident report prints, so it is the tool
// talking to an operator mid-incident. Changing it changes what MeshMedic
// says, and that deserves review. What is genuinely free is YAML the parser
// throws away.
func TestLockIgnoresChangesThatNeverReachTheParsedEntry(t *testing.T) {
	scenarios, _ := shipped(t)
	lock, err := catalog.LoadLock("../../catalog.lock")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	before := mustVerify(t, lock, scenarios)

	// Rewrite every entry with added comments and blank lines, which the YAML
	// parser discards.
	dir := t.TempDir()
	sources, err := filepath.Glob("../../catalog/*.yaml")
	if err != nil || len(sources) == 0 {
		t.Fatalf("no catalog files to reformat: %v", err)
	}
	for _, src := range sources {
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		reformatted := "# a comment added by the mutation suite\n\n" +
			strings.ReplaceAll(string(body), "\nsignal:", "\n\n# another comment\nsignal:")
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(src)), []byte(reformatted), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	reloaded, err := catalog.LoadDir(dir)
	if err != nil {
		t.Fatalf("reloading the reformatted catalog: %v", err)
	}
	if len(reloaded) != len(scenarios) {
		t.Fatalf("reformatting changed the entry count: %d then %d", len(scenarios), len(reloaded))
	}

	for id, st := range mustVerify(t, lock, reloaded) {
		if st != before[id] {
			t.Errorf("%s: comments and blank lines moved the hash from %s to %s",
				id, before[id], st)
		}
	}
}

// The other half of the same claim: a change that does reach the parsed entry
// must move the hash, even when it looks cosmetic. The Diagnosis paragraph is
// the clearest case, because it reads like prose and is in fact output.
func TestLockCatchesAChangeToWhatTheReportSays(t *testing.T) {
	scenarios, _ := shipped(t)
	lock, err := catalog.LoadLock("../../catalog.lock")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	locked := mustVerify(t, lock, scenarios)

	for i := range scenarios {
		if locked[scenarios[i].ID] != catalog.StatusLocked {
			continue
		}
		mutants := append([]catalog.Scenario(nil), scenarios...)
		mutants[i].Description = "This entry is harmless and can be ignored."
		if mustVerify(t, lock, mutants)[mutants[i].ID] != catalog.StatusMismatch {
			t.Errorf("%s: rewriting the diagnosis an operator reads did not move the hash",
				mutants[i].ID)
		}
	}
}

func mustVerify(t *testing.T, lock catalog.Lock, scenarios []catalog.Scenario) map[string]catalog.LockStatus {
	t.Helper()
	st, err := lock.Verify(scenarios)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return st
}
