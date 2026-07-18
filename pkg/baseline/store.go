// Package baseline tracks each signal's normal value over time so a scenario
// can fire on a deviation from a target's own baseline instead of a fixed
// threshold. The store is an exponentially-weighted moving average per key,
// persisted to disk so the learned normal survives a restart.
package baseline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Stat is one key's learned normal: the EWMA of its observed values and how
// many observations have gone into it.
type Stat struct {
	EWMA  float64 `json:"ewma"`
	Count int     `json:"count"`
}

// Store holds a baseline per key. It is safe for concurrent use. A nil Store
// is not valid; use New.
type Store struct {
	mu    sync.Mutex
	alpha float64
	stats map[string]*Stat
	path  string
}

// New builds a store backed by path, smoothing observations with alpha. A
// small alpha (e.g. 0.05) makes the baseline slow to move and resistant to
// transient spikes, which is what "normal" should be.
func New(path string, alpha float64) *Store {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.05
	}
	return &Store{alpha: alpha, stats: map[string]*Stat{}, path: path}
}

// Observe folds one value into the key's baseline.
func (s *Store) Observe(key string, value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats[key]
	if st == nil {
		s.stats[key] = &Stat{EWMA: value, Count: 1}
		return
	}
	st.EWMA = s.alpha*value + (1-s.alpha)*st.EWMA
	st.Count++
}

// Baseline returns the key's learned normal and whether it has enough
// observations (>= minSamples) to be trusted. A caller that gets ready=false
// must fall back to a static threshold: acting on an unlearned baseline is
// how a "relative" alert fires on noise during warm-up.
func (s *Store) Baseline(key string, minSamples int) (value float64, ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats[key]
	if st == nil || st.Count < minSamples {
		return 0, false
	}
	return st.EWMA, true
}

// Load reads a persisted store. A missing file is not an error: a fresh
// deployment starts with no baseline and learns from scratch.
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.stats)
}

// Save writes the store atomically: a temp file in the same directory then a
// rename, so a crash mid-write cannot leave a truncated baseline behind.
func (s *Store) Save() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.stats, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".baseline-*.tmp")
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
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("baseline: rename %s: %w", s.path, err)
	}
	return nil
}
