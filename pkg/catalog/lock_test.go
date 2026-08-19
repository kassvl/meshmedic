package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const lockFixture = `
id: test-entry
title: A test entry
severity: critical
description: something went wrong
signal:
  promql: sum(rate(istio_requests_total[2m]))
  comparison: ">"
  threshold: 0.15
  for: 120s
evidence:
  - name: by-workload
    promql: sum by (destination_workload) (rate(istio_requests_total[5m]))
remediation:
  target:
    apiVersion: networking.istio.io/v1
    kind: DestinationRule
  action: enable-outlier-detection
  patchTemplate: |
    apiVersion: networking.istio.io/v1
    kind: DestinationRule
    metadata:
      name: payments-outlier
rollback: revert it
guardrails:
  requiresApproval: true
  maxAppliesPerHour: 2
`

func mustParse(t *testing.T, src string) Scenario {
	t.Helper()
	var s Scenario
	if err := yaml.Unmarshal([]byte(src), &s); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return s
}

// This is where a lock design usually rots: someone reformats a YAML file,
// every hash changes, and the team learns to re-approve without reading.
// Cosmetic edits must not change the hash; semantic edits must.
func TestHashIsIdempotentAcrossCosmeticEdits(t *testing.T) {
	base := mustParse(t, lockFixture)
	want, err := Hash(base)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	cosmetic := map[string]string{
		"a comment added": "# this entry is load-bearing\n" + lockFixture,
		"keys reordered": strings.Replace(lockFixture,
			"id: test-entry\ntitle: A test entry\n",
			"title: A test entry\nid: test-entry\n", 1),
		"blank lines and indentation churn": strings.ReplaceAll(lockFixture, "\nsignal:", "\n\n\nsignal:"),
		"quoted scalars":                    strings.Replace(lockFixture, "id: test-entry", `id: "test-entry"`, 1),
		// Trailing whitespace on a plain scalar is stripped by YAML, so it is
		// cosmetic. Inside a block scalar it is not: see the semantic table.
		"trailing whitespace on plain scalars": strings.Replace(lockFixture,
			"id: test-entry\ntitle: A test entry\n",
			"id: test-entry   \ntitle: A test entry  \n", 1),
	}
	for name, src := range cosmetic {
		t.Run(name, func(t *testing.T) {
			got, err := Hash(mustParse(t, src))
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}
			if got != want {
				t.Errorf("hash changed on a cosmetic edit (%s); every entry would need re-approval for nothing", name)
			}
		})
	}
}

func TestHashChangesOnEverySemanticEdit(t *testing.T) {
	base := mustParse(t, lockFixture)
	baseHash, _ := Hash(base)

	semantic := map[string]string{
		"threshold moved":     strings.Replace(lockFixture, "threshold: 0.15", "threshold: 0.05", 1),
		"signal query edited": strings.Replace(lockFixture, "[2m]", "[10m]", 1),
		"hold duration cut":   strings.Replace(lockFixture, "for: 120s", "for: 10s", 1),
		"patch template":      strings.Replace(lockFixture, "name: payments-outlier", "name: payments-ejection", 1),
		"comparison flipped":  strings.Replace(lockFixture, `comparison: ">"`, `comparison: "<"`, 1),
		"guardrail loosened":  strings.Replace(lockFixture, "maxAppliesPerHour: 2", "maxAppliesPerHour: 99", 1),
		"approval waived":     strings.Replace(lockFixture, "requiresApproval: true", "requiresApproval: false", 1),
		"evidence removed":    strings.Replace(lockFixture, "  - name: by-workload\n    promql: sum by (destination_workload) (rate(istio_requests_total[5m]))\n", "", 1),
		// Whitespace inside a literal block scalar is content, not layout:
		// YAML preserves it, so it lands in the rendered patch. Treating it
		// as cosmetic would let an edit to the patch body slip past the lock.
		"trailing whitespace inside the patch template": strings.Replace(lockFixture,
			"      name: payments-outlier\n", "      name: payments-outlier   \n", 1),
	}
	for name, src := range semantic {
		t.Run(name, func(t *testing.T) {
			got, err := Hash(mustParse(t, src))
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}
			if got == baseHash {
				t.Errorf("hash unchanged after a semantic edit (%s); the lock would approve something nobody reviewed", name)
			}
		})
	}
}

func TestVerifyReportsMissingMismatchAndLocked(t *testing.T) {
	s := mustParse(t, lockFixture)
	lock := Lock{Version: LockVersion, Entries: map[string]LockEntry{}}

	status, err := lock.Verify([]Scenario{s})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if status[s.ID] != StatusMissing {
		t.Errorf("unapproved entry = %q, want %q", status[s.ID], StatusMissing)
	}

	if err := lock.Approve(s, Validation{Istio: "1.24.1", Testbed: "aa818f1"}, time.Now()); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	status, _ = lock.Verify([]Scenario{s})
	if status[s.ID] != StatusLocked {
		t.Errorf("approved entry = %q, want %q", status[s.ID], StatusLocked)
	}

	// The acceptance criterion: mutate a threshold and the entry stops being
	// the entry that was approved.
	edited := mustParse(t, strings.Replace(lockFixture, "threshold: 0.15", "threshold: 0.9", 1))
	status, _ = lock.Verify([]Scenario{edited})
	if status[edited.ID] != StatusMismatch {
		t.Errorf("edited entry = %q, want %q", status[edited.ID], StatusMismatch)
	}
}

func TestApproveRefusesWithoutRecordedValidation(t *testing.T) {
	s := mustParse(t, lockFixture)
	lock := Lock{Version: LockVersion, Entries: map[string]LockEntry{}}

	if err := lock.Approve(s, Validation{Testbed: "aa818f1"}, time.Now()); err == nil {
		t.Error("approved an entry with no Istio version recorded")
	}
	if err := lock.Approve(s, Validation{Istio: "1.24.1"}, time.Now()); err == nil {
		t.Error("approved an entry with no testbed commit recorded")
	}
	if len(lock.Entries) != 0 {
		t.Errorf("a refused approval still wrote %d entries", len(lock.Entries))
	}
}

func TestLockRoundTripsAndDigestCoversRemoval(t *testing.T) {
	s := mustParse(t, lockFixture)
	lock := Lock{Version: LockVersion, Entries: map[string]LockEntry{}}
	if err := lock.Approve(s, Validation{Istio: "1.24.1", Testbed: "aa818f1"}, time.Now()); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	withEntry := Digest(lock.Entries)

	path := filepath.Join(t.TempDir(), "catalog.lock")
	if err := lock.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadLock(path)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if loaded.Entries[s.ID].SHA256 != lock.Entries[s.ID].SHA256 {
		t.Error("hash did not survive the round trip")
	}
	if loaded.Digest != withEntry {
		t.Errorf("digest = %q, want %q", loaded.Digest, withEntry)
	}
	if loaded.Entries[s.ID].ValidatedAgainst.Istio != "1.24.1" {
		t.Error("validated_against did not survive the round trip")
	}

	// A removed entry must move the catalog-level digest, or a deletion is
	// invisible to anyone diffing the lock.
	delete(loaded.Entries, s.ID)
	if Digest(loaded.Entries) == withEntry {
		t.Error("digest unchanged after removing an entry")
	}
}

func TestStaleFindsApprovalsWithoutAnEntry(t *testing.T) {
	s := mustParse(t, lockFixture)
	lock := Lock{Version: LockVersion, Entries: map[string]LockEntry{
		"deleted-entry": {SHA256: "sha256:whatever"},
	}}
	_ = lock.Approve(s, Validation{Istio: "1.24.1", Testbed: "aa818f1"}, time.Now())

	stale := lock.Stale([]Scenario{s})
	if len(stale) != 1 || stale[0] != "deleted-entry" {
		t.Errorf("stale = %v, want [deleted-entry]: an approval must not outlive its entry", stale)
	}
}

func TestLoadLockRejectsAFutureFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.lock")
	if err := os.WriteFile(path, []byte(`{"version": 99, "entries": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(path); err == nil {
		t.Error("loaded a lock written by a newer format without complaint")
	}
}

// Every entry in the real catalog must hash, and no two entries may collide.
func TestRealCatalogHashesUniquely(t *testing.T) {
	scenarios, err := LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	seen := map[string]string{}
	for _, s := range scenarios {
		h, err := Hash(s)
		if err != nil {
			t.Fatalf("Hash(%s): %v", s.ID, err)
		}
		if prev, dup := seen[h]; dup {
			t.Errorf("%s and %s hash identically", s.ID, prev)
		}
		seen[h] = s.ID
	}
	if len(seen) != len(scenarios) {
		t.Errorf("hashed %d distinct entries, want %d", len(seen), len(scenarios))
	}
}
