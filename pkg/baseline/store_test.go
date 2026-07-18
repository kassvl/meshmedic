package baseline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEWMAConvergesAndSmooths(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "b.json"), 0.5)
	for i := 0; i < 10; i++ {
		s.Observe("k", 100)
	}
	v, ready := s.Baseline("k", 5)
	if !ready {
		t.Fatal("want ready after 10 observations")
	}
	if v < 99 || v > 101 {
		t.Fatalf("EWMA %v, want ~100 after steady input", v)
	}
	// A single spike must not move a slow baseline far.
	slow := New(filepath.Join(t.TempDir(), "b2.json"), 0.05)
	for i := 0; i < 50; i++ {
		slow.Observe("k", 100)
	}
	slow.Observe("k", 10000)
	v, _ = slow.Baseline("k", 5)
	if v > 700 {
		t.Fatalf("EWMA %v moved too far on one spike; a slow baseline should resist it", v)
	}
}

func TestBaselineNotReadyBeforeMinSamples(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "b.json"), 0.1)
	s.Observe("k", 42)
	s.Observe("k", 42)
	if _, ready := s.Baseline("k", 5); ready {
		t.Fatal("baseline must not be ready before minSamples; that is the warm-up guardrail")
	}
	if _, ready := s.Baseline("missing", 1); ready {
		t.Fatal("an unseen key must never be ready")
	}
}

func TestPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	s := New(path, 0.2)
	for i := 0; i < 8; i++ {
		s.Observe("payments/canary", 250)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	loaded := New(path, 0.2)
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	v, ready := loaded.Baseline("payments/canary", 5)
	if !ready || v < 249 || v > 251 {
		t.Fatalf("loaded baseline %v ready=%v, want ~250 ready", v, ready)
	}
}

func TestLoadMissingFileIsClean(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "does-not-exist.json"), 0.1)
	if err := s.Load(); err != nil {
		t.Fatalf("loading a missing baseline should be clean, got %v", err)
	}
	if _, ready := s.Baseline("k", 1); ready {
		t.Fatal("nothing should be ready after loading a missing file")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	// After a save, the directory holds only the final file, no temp litter.
	dir := t.TempDir()
	s := New(filepath.Join(dir, "b.json"), 0.1)
	s.Observe("k", 1)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "b.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("dir has %v, want only b.json (atomic rename left no temp)", names)
	}
}
