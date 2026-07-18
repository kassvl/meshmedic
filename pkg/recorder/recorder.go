// Package recorder appends fingerprints of anomalies that no catalog entry
// explains, so the taxonomy can grow from what the tool actually sees in
// production. It records only: a fingerprint is raw material for a human to
// review and, if it is real, turn into a validated catalog entry. Nothing
// here drives remediation. That guardrail is the whole point: learning from
// unverified signals is how a tool starts confidently fixing non-problems.
package recorder

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Fingerprint is one recorded anomaly: a watched signal that deviated from
// its learned baseline while no catalog scenario fired for the target.
type Fingerprint struct {
	Time     time.Time         `json:"time"`
	Target   map[string]string `json:"target"`
	Signal   string            `json:"signal"`
	Value    float64           `json:"value"`
	Baseline float64           `json:"baseline"`
	Factor   float64           `json:"factor"` // value / baseline, how far off normal
}

// Recorder appends fingerprints to a JSONL file, one per line.
type Recorder struct {
	mu   sync.Mutex
	path string
}

// New builds a recorder writing to path. The file is created on first write.
func New(path string) *Recorder {
	return &Recorder{path: path}
}

// Record appends one fingerprint as a JSON line. Append-only so the log is a
// durable, ordered trail of what the tool could not explain.
func (r *Recorder) Record(fp Fingerprint) error {
	if fp.Time.IsZero() {
		fp.Time = time.Now().UTC()
	}
	line, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}
