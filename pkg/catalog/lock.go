package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The safety claim is that every fix comes from a reviewed catalog entry. But
// `catalog/` is read directly at startup, which makes it simultaneously the
// editable surface and the enforced artifact: nothing establishes that the
// entry running is the entry that was reviewed and validated on a testbed.
// catalog.lock is what separates the two. Accepted must not degrade into
// merely committed.

// LockVersion is the on-disk format version of catalog.lock.
const LockVersion = 1

// Validation records what an entry was validated against, so a lock entry
// carries the context that makes it meaningful. An entry validated against
// Istio 1.24.1 says nothing about 1.30 and should not pretend to.
type Validation struct {
	Istio   string `json:"istio"`   // Istio version the signal was observed on
	Testbed string `json:"testbed"` // testbed commit the fault was injected from
}

// LockEntry is one approved catalog entry.
type LockEntry struct {
	SHA256           string     `json:"sha256"`
	ValidatedAt      time.Time  `json:"validated_at"`
	ValidatedAgainst Validation `json:"validated_against"`
}

// Lock is the committed record of which catalog entries have been reviewed
// and testbed-validated, and in exactly which form.
type Lock struct {
	Version int                  `json:"version"`
	Digest  string               `json:"digest"` // over the sorted per-entry hashes
	Entries map[string]LockEntry `json:"entries"`
}

// LockStatus is one entry's standing against the lock.
type LockStatus string

const (
	// StatusLocked: the entry's hash matches the lock. It runs.
	StatusLocked LockStatus = "locked"
	// StatusMissing: the entry is not in the lock at all. Never approved.
	StatusMissing LockStatus = "missing"
	// StatusMismatch: the entry is in the lock under a different hash. It was
	// edited after approval, so what is on disk is not what was reviewed.
	StatusMismatch LockStatus = "mismatch"
)

// Hash returns the entry's content hash.
//
// It hashes the *parsed* scenario rather than the file bytes, which is what
// makes the property that matters hold: reformatting a YAML file, reordering
// its keys, or editing a comment does not change the hash, while changing a
// threshold, a query, or a patch template does. A byte hash would break on
// every cosmetic edit and quietly train people to re-approve without reading,
// which is the failure mode this whole mechanism exists to prevent.
func Hash(s Scenario) (string, error) {
	// The Scenario struct has a fixed field order and contains no maps, so
	// its JSON encoding is deterministic without further canonicalization.
	data, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("hashing %s: %w", s.ID, err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Digest folds the per-entry hashes into one catalog-level value, so a
// removed entry is as visible as a changed one.
func Digest(entries map[string]LockEntry) string {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		fmt.Fprintf(h, "%s=%s\n", id, entries[id].SHA256)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// LoadLock reads a lock file. A missing file yields an empty lock rather than
// an error: a catalog with no lock is a legitimate starting state, and the
// caller decides how loudly to say so.
func LoadLock(path string) (Lock, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Lock{Version: LockVersion, Entries: map[string]LockEntry{}}, nil
	}
	if err != nil {
		return Lock{Version: LockVersion, Entries: map[string]LockEntry{}}, err
	}
	return ParseLock(data, path)
}

// ParseLock decodes lock bytes from anywhere, so a binary carrying its own
// embedded lock validates it exactly as it would one read from disk.
func ParseLock(data []byte, path string) (Lock, error) {
	l := Lock{Version: LockVersion, Entries: map[string]LockEntry{}}
	if err := json.Unmarshal(data, &l); err != nil {
		return l, fmt.Errorf("%s: %w", path, err)
	}
	if l.Entries == nil {
		l.Entries = map[string]LockEntry{}
	}
	if l.Version != LockVersion {
		return l, fmt.Errorf("%s: lock version %d, this build understands %d", path, l.Version, LockVersion)
	}
	return l, nil
}

// Save writes the lock atomically, recomputing the catalog-level digest.
func (l Lock) Save(path string) error {
	l.Version = LockVersion
	l.Digest = Digest(l.Entries)
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".catalog-lock-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("lock: rename %s: %w", path, err)
	}
	return nil
}

// Verify reports each scenario's standing against the lock.
func (l Lock) Verify(scenarios []Scenario) (map[string]LockStatus, error) {
	out := make(map[string]LockStatus, len(scenarios))
	for _, s := range scenarios {
		h, err := Hash(s)
		if err != nil {
			return nil, err
		}
		locked, ok := l.Entries[s.ID]
		switch {
		case !ok:
			out[s.ID] = StatusMissing
		case locked.SHA256 != h:
			out[s.ID] = StatusMismatch
		default:
			out[s.ID] = StatusLocked
		}
	}
	return out, nil
}

// Stale returns lock entries that no longer correspond to any scenario on
// disk, so an approval cannot outlive the entry it approved.
func (l Lock) Stale(scenarios []Scenario) []string {
	present := make(map[string]bool, len(scenarios))
	for _, s := range scenarios {
		present[s.ID] = true
	}
	var stale []string
	for id := range l.Entries {
		if !present[id] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	return stale
}

// Approve records one entry as reviewed and testbed-validated. It is the only
// operation that writes a lock, and it refuses an entry with no recorded
// validation: an approval that does not say what the entry was validated
// against is a rubber stamp, and a rubber stamp is worse than no lock at all
// because it looks like a guarantee.
func (l Lock) Approve(s Scenario, v Validation, at time.Time) error {
	if v.Istio == "" {
		return fmt.Errorf("%s: refusing to approve without validated_against.istio: an approval that does not say what the entry was validated on is a rubber stamp", s.ID)
	}
	if v.Testbed == "" {
		return fmt.Errorf("%s: refusing to approve without validated_against.testbed: the testbed commit is what makes the validation reproducible", s.ID)
	}
	h, err := Hash(s)
	if err != nil {
		return err
	}
	l.Entries[s.ID] = LockEntry{SHA256: h, ValidatedAt: at.UTC(), ValidatedAgainst: v}
	return nil
}
