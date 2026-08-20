package proof

import (
	"fmt"
	"sort"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
)

// minimumSlack is how much room a proof needs between the moment its entry
// could first report and its deadline.
//
// After the hold duration elapses the detector still needs a tick to deliver,
// and before it the signal has to climb into breach at all, which a rate
// window makes gradual. Under a minute of slack leaves roughly four ticks at
// the default poll for both of those, which is a proof that will eventually
// fail for timing rather than for the entry.
const minimumSlack = time.Minute

// Winnability is one spec measured against the entry it proves.
type Winnability struct {
	Entry       string
	FiresWithin time.Duration
	Hold        time.Duration
	// Slack is how long the proof allows beyond the entry's hold duration.
	// Negative or zero means the deadline is inside the hold and the proof
	// cannot be won no matter how correct the entry is.
	Slack time.Duration
}

// Unwinnable reports whether the deadline is inside the hold duration.
func (w Winnability) Unwinnable() bool { return w.Slack <= 0 }

// Thin reports whether the proof can be won but only just.
func (w Winnability) Thin() bool { return !w.Unwinnable() && w.Slack < minimumSlack }

func (w Winnability) String() string {
	switch {
	case w.Unwinnable():
		return fmt.Sprintf("%s: firesWithin is %s but the entry holds for %s, so the deadline is inside the hold and no correct entry can meet it",
			w.Entry, w.FiresWithin, w.Hold)
	case w.Thin():
		return fmt.Sprintf("%s: firesWithin is %s against a %s hold, leaving %s. The signal has to climb into breach and the detector needs a tick to deliver, both inside that",
			w.Entry, w.FiresWithin, w.Hold, w.Slack)
	}
	return fmt.Sprintf("%s: %s slack over a %s hold", w.Entry, w.Slack, w.Hold)
}

// CheckWinnable measures every spec against the hold duration of the entry it
// proves, and returns the ones that cannot be won and the ones that only just
// can.
//
// Spec's own documentation has always said that firesWithin must exceed the
// hold or the proof is unwinnable, and nothing enforced it: Validate checks
// that the deadline is positive and stops there, because a spec on its own
// cannot see the catalog. So the rule lived in a comment, and holding to it
// was a matter of remembering.
//
// That is not hypothetical maintenance worry. On 2026-08-20 an entry's hold
// went from 90 seconds to five minutes, and its proof's five-minute deadline
// had to be raised by hand in the same edit. Forgetting would have produced a
// run that failed with "never fired within 5m", which reads as a broken entry
// and is in fact a deadline that expired at the exact moment the entry became
// eligible to report. The measurement would have been about arithmetic and
// looked like it was about the mesh.
//
// Nothing in the repository violates this today. This is a guard on tomorrow.
func CheckWinnable(scenarios []catalog.Scenario, specs []Spec) []Winnability {
	holds := make(map[string]time.Duration, len(scenarios))
	for _, s := range scenarios {
		holds[s.ID] = detect.Hold(s)
	}

	out := make([]Winnability, 0, len(specs))
	for _, sp := range specs {
		hold, known := holds[sp.Entry]
		if !known {
			// An unknown entry is already an error elsewhere, and reporting
			// it again as a timing problem would send someone to the wrong
			// end of the spec.
			continue
		}
		out = append(out, Winnability{
			Entry:       sp.Entry,
			FiresWithin: sp.FiresWithin.D(),
			Hold:        hold,
			Slack:       sp.FiresWithin.D() - hold,
		})
	}
	// Tightest first: the one worth reading is the one closest to the edge.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slack != out[j].Slack {
			return out[i].Slack < out[j].Slack
		}
		return out[i].Entry < out[j].Entry
	})
	return out
}

// CheckEntriesExist reports proof specs naming an entry the catalog does not
// have.
//
// A proof for an entry that is not in the catalog proves nothing, and a typo
// in an id silently skips the entry it meant to test, which is the quiet
// version of the failure this whole harness exists to prevent. It lives here
// rather than inline in the command so a mutation suite can rename an entry
// and confirm the check notices.
func CheckEntriesExist(scenarios []catalog.Scenario, specs []Spec) []string {
	known := make(map[string]bool, len(scenarios))
	for _, s := range scenarios {
		known[s.ID] = true
	}
	var missing []string
	for _, sp := range specs {
		if !known[sp.Entry] {
			missing = append(missing, sp.Entry)
		}
	}
	sort.Strings(missing)
	return missing
}
