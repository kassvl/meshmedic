package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
)

// defaultLockPath resolves catalog.lock next to the catalog it locks, which
// keeps the two from drifting apart when someone points --catalog elsewhere.
func defaultLockPath(catalogDir string) string {
	return catalogDir + ".lock"
}

// runApprove records catalog entries as reviewed and testbed-validated. It is
// the only operation that writes the lock, and it is deliberately manual: the
// signature of an approval is that a human ran it after watching the fault
// fire on a testbed. Nothing here inspects a cluster or infers validation.
func runApprove(args []string) {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	dir := fs.String("catalog", defaultCatalogDir(), "catalog directory (or $MESHMEDIC_CATALOG)")
	lockPath := fs.String("lock", "", "lock file (default <catalog>.lock)")
	istio := fs.String("istio", "", "Istio version the entry's signal was observed on")
	testbed := fs.String("testbed", "", "testbed commit the fault was injected from")
	all := fs.Bool("all", false, "approve every entry whose hash is missing or stale")
	prune := fs.Bool("prune", false, "drop lock entries that no longer have a catalog entry")
	var only multiFlag
	fs.Var(&only, "scenario", "scenario id to approve (repeatable)")
	fs.Parse(args)

	// approve is the one command that cannot fall back to the embedded
	// catalog: approving writes a lock file, and a catalog compiled into a
	// binary has no directory to write beside. Saying so beats failing with
	// a path error that does not explain itself.
	if *dir == "" {
		fmt.Fprintln(os.Stderr,
			"approve needs --catalog pointing at a catalog directory on disk.\nThe catalog embedded in this binary is already approved and cannot be re-approved in place.")
		os.Exit(2)
	}
	if *lockPath == "" {
		*lockPath = defaultLockPath(*dir)
	}
	scenarios, err := catalog.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog invalid:", err)
		os.Exit(1)
	}
	lock, err := catalog.LoadLock(*lockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lock invalid:", err)
		os.Exit(1)
	}

	if *prune {
		stale := lock.Stale(scenarios)
		for _, id := range stale {
			delete(lock.Entries, id)
			fmt.Printf("pruned %s: approved but no longer in the catalog\n", id)
		}
		if len(stale) == 0 {
			fmt.Println("nothing to prune: every approval has an entry")
		}
	}

	wanted := map[string]bool{}
	for _, id := range only {
		wanted[id] = true
	}
	if len(wanted) == 0 && !*all {
		if !*prune {
			fmt.Fprintln(os.Stderr, "pass --scenario <id> (repeatable) or --all; approving is a deliberate act")
			os.Exit(2)
		}
		if err := lock.Save(*lockPath); err != nil {
			fmt.Fprintln(os.Stderr, "writing lock:", err)
			os.Exit(1)
		}
		return
	}

	status, err := lock.Verify(scenarios)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	now := time.Now()
	approved := 0
	for _, s := range scenarios {
		if len(wanted) > 0 && !wanted[s.ID] {
			continue
		}
		if *all && status[s.ID] == catalog.StatusLocked {
			continue
		}
		if err := lock.Approve(s, catalog.Validation{Istio: *istio, Testbed: *testbed}, now); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("approved %s (was %s)\n", s.ID, status[s.ID])
		approved++
	}
	for id := range wanted {
		found := false
		for _, s := range scenarios {
			if s.ID == id {
				found = true
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "unknown scenario %q\n", id)
			os.Exit(1)
		}
	}
	if err := lock.Save(*lockPath); err != nil {
		fmt.Fprintln(os.Stderr, "writing lock:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d entries approved, %d locked in total\n", *lockPath, approved, len(lock.Entries))
}

// unlockedFrom verifies the catalog against an already-loaded lock and returns
// the entries that must not run, mapped to why. Shared by watch and validate
// so the two can never disagree about what is covered.
func unlockedFrom(lock catalog.Lock, scenarios []catalog.Scenario, lockPath string) map[string]string {
	status, err := lock.Verify(scenarios)
	if err != nil {
		// Hashing a loaded scenario cannot fail; treating it as fully
		// unlocked would be the safe reading if it ever did.
		return map[string]string{}
	}
	unlocked := map[string]string{}
	for id, st := range status {
		switch st {
		case catalog.StatusMissing:
			unlocked[id] = "not in " + lockPath + ": never approved, so it is not an entry anyone reviewed"
		case catalog.StatusMismatch:
			unlocked[id] = "hash does not match " + lockPath + ": edited after approval, so what is on disk is not what was reviewed"
		}
	}
	return unlocked
}

// printLockReport writes the per-entry lock standing as a table.
func printLockReport(scenarios []catalog.Scenario, lock catalog.Lock, unlocked map[string]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLOCK\tVALIDATED AGAINST")
	for _, s := range scenarios {
		state := "locked"
		if _, bad := unlocked[s.ID]; bad {
			state = "UNLOCKED"
		}
		against := "-"
		if e, ok := lock.Entries[s.ID]; ok && e.ValidatedAgainst.Istio != "" {
			against = fmt.Sprintf("istio %s, testbed %s", e.ValidatedAgainst.Istio, e.ValidatedAgainst.Testbed)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.ID, state, against)
	}
	w.Flush()

	if stale := lock.Stale(scenarios); len(stale) > 0 {
		sort.Strings(stale)
		fmt.Printf("\n%d approvals have no catalog entry (run `meshmedic approve --prune`): %v\n", len(stale), stale)
	}
}
