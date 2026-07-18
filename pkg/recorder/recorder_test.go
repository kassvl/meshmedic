package recorder

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unmatched.jsonl")
	r := New(path)

	if err := r.Record(Fingerprint{
		Target: map[string]string{"service": "payments"}, Signal: "5xx-rate",
		Value: 3.0, Baseline: 0.1, Factor: 30,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Record(Fingerprint{
		Target: map[string]string{"service": "payments"}, Signal: "p99",
		Value: 900, Baseline: 50, Factor: 18,
	}); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []Fingerprint
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var fp Fingerprint
		if err := json.Unmarshal(sc.Bytes(), &fp); err != nil {
			t.Fatalf("line is not valid JSON: %v", err)
		}
		lines = append(lines, fp)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (append-only)", len(lines))
	}
	if lines[0].Signal != "5xx-rate" || lines[1].Signal != "p99" {
		t.Fatalf("order not preserved: %v", lines)
	}
	if lines[0].Time.IsZero() {
		t.Fatal("Record should stamp a time when none is given")
	}
}

func TestRecordPreservesGivenTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	r := New(path)
	when := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	if err := r.Record(Fingerprint{Time: when, Signal: "x", Value: 1, Baseline: 1, Factor: 1}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var fp Fingerprint
	if err := json.Unmarshal(data[:len(data)-1], &fp); err != nil {
		t.Fatal(err)
	}
	if !fp.Time.Equal(when) {
		t.Fatalf("time %v, want the given %v", fp.Time, when)
	}
}
