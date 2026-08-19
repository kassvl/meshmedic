// Command meshmedic-prove runs the end-to-end evidence for catalog entries:
// it injects each entry's fault into a live cluster, watches a real detector,
// and asserts that the entry fired, named the culprit, kept its neighbours
// quiet, and cleared on reset.
//
// It is a separate binary, and ships as its own separately named release
// artifact, because two claims are in tension and both matter.
//
// The detector's claim is that it holds no cluster write credentials. Nothing
// that runs continuously inside someone's mesh should carry a fault-injection
// tool: a compromised pod would hand an attacker a ready-made one. So this is
// absent from the meshmedic archive and from the container image.
//
// The benchmark's claim is reproducibility, and proofs only a maintainer can
// run are unfalsifiable, which is the opposite of what this project argues
// for. So it is downloadable, by name, with what it does written on it.
//
// A separate, clearly-labelled artifact serves both: anyone can reproduce the
// evidence, and nobody gets fault injection by accident. It refuses to run
// without an explicit acknowledgement, after printing every command it is
// about to run against the cluster.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
	"github.com/kassvl/meshmedic/pkg/proof"
)

func main() {
	fs := flag.NewFlagSet("meshmedic-prove", flag.ExitOnError)
	catalogDir := fs.String("catalog", "catalog", "catalog directory")
	proofDir := fs.String("proofs", "proof", "proof spec directory")
	promURL := fs.String("prometheus", "http://127.0.0.1:9090", "Prometheus base URL")
	only := fs.String("entry", "", "prove one entry instead of all of them")
	list := fs.Bool("list", false, "list the entries that have a proof, and those that do not")
	outDir := fs.String("out", "", "write each incident report under this directory")
	yes := fs.Bool("yes-inject-faults", false, "acknowledge that this mutates the cluster it is pointed at")
	kubeContext := fs.String("kube-context", os.Getenv("MESHMEDIC_KUBE_CONTEXT"), "kubeconfig context to read through; must be the cluster the inject commands target")
	quiesce := fs.Duration("quiesce", 150*time.Second, "settle time between proofs, so one proof's fault does not leak into the next")
	fs.Parse(os.Args[1:])

	scenarios, err := catalog.LoadDir(*catalogDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog invalid:", err)
		os.Exit(1)
	}
	specs, err := proof.LoadDir(*proofDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proofs invalid:", err)
		os.Exit(1)
	}

	if *list {
		listCoverage(scenarios, specs)
		return
	}

	// A proof for an entry that is not in the catalog proves nothing, and a
	// typo in an id would otherwise silently skip the entry it meant to test.
	known := map[string]bool{}
	for _, s := range scenarios {
		known[s.ID] = true
	}
	for _, sp := range specs {
		if !known[sp.Entry] {
			fmt.Fprintf(os.Stderr, "proof references unknown entry %q\n", sp.Entry)
			os.Exit(1)
		}
	}

	if *only != "" {
		filtered := specs[:0]
		for _, sp := range specs {
			if sp.Entry == *only {
				filtered = append(filtered, sp)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "no proof for entry %q; run --list to see what is covered\n", *only)
			os.Exit(1)
		}
		specs = filtered
	}

	// Print exactly what is about to be done to the cluster, then require an
	// acknowledgement. A tool that mutates a mesh should never do it because
	// someone pasted a command without reading it, and the list below is the
	// difference between an informed act and an accident.
	if !confirmed(specs, *yes) {
		os.Exit(2)
	}

	logger := log.New(os.Stderr, "prove: ", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A proof without cluster reads cannot see configuration, log or rollout
	// evidence, which is the entire deliverable of every triage entry. Failing
	// here is right: a run that silently proves less than it claims is worse
	// than a run that does not start.
	reader, err := kube.NewReaderForContext(*kubeContext)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"cannot read the cluster (%v).\nA proof without kubectl skips configuration, log and rollout evidence, so it would prove less than it claims.\n", err)
		os.Exit(1)
	}
	if *kubeContext != "" {
		logger.Printf("reading the cluster through context %s", *kubeContext)
	} else {
		logger.Printf("reading through kubectl's current context; pass --kube-context if the inject commands target a different cluster")
	}
	runner := proof.NewRunner(scenarios, prom.NewClient(*promURL), reader)

	results := make([]proof.Result, 0, len(specs))
	for i, sp := range specs {
		// Quiesce between proofs. A reset takes time to propagate and the
		// rate windows in these signals are two minutes wide, so a proof that
		// starts the instant the previous one finished is measuring the tail
		// of someone else's fault. Skipping this is not theoretical:
		// authz-deny-flood passes in 1m2s on its own and did not fire at all
		// within six minutes when it followed traffic-vanished-triage by
		// sixteen seconds, because loadgen was still coming back and there
		// were no requests left to deny.
		if i > 0 && *quiesce > 0 {
			logger.Printf("quiescing %s so the previous fault decays out of the rate windows", *quiesce)
			select {
			case <-ctx.Done():
			case <-time.After(*quiesce):
			}
		}
		logger.Printf("[%d/%d] %s: %s", i+1, len(specs), sp.Entry, sp.Summary)
		runner.Log = func(format string, args ...any) {
			logger.Printf("  "+sp.Entry+": "+format, args...)
		}
		res := runner.Run(ctx, sp)
		results = append(results, res)

		verdict := "PASS"
		if !res.Passed {
			verdict = "FAIL"
		}
		logger.Printf("[%d/%d] %s: %s (%s)", i+1, len(specs), sp.Entry, verdict, res.Duration.Round(time.Second))
		for _, f := range res.Failures {
			logger.Printf("    %s", f)
		}
		if *outDir != "" && res.Report != "" {
			writeReport(*outDir, sp.Entry, res.Report, logger)
		}
		if ctx.Err() != nil {
			logger.Printf("interrupted; %d proofs not run", len(specs)-i-1)
			break
		}
	}

	os.Exit(summarize(results, scenarios, specs))
}

// confirmed prints every command that will be run against the cluster and
// returns whether the operator has acknowledged it.
func confirmed(specs []proof.Spec, acknowledged bool) bool {
	fmt.Fprintln(os.Stderr, "meshmedic-prove INJECTS FAULTS into the cluster it is pointed at.")
	fmt.Fprintf(os.Stderr, "It will run the following against %d entries:\n\n", len(specs))
	for _, sp := range specs {
		fmt.Fprintf(os.Stderr, "  %s\n", sp.Entry)
		for _, c := range sp.Inject {
			fmt.Fprintf(os.Stderr, "    inject  %s\n", strings.Join(c.Run, " "))
		}
		for _, c := range sp.Reset {
			fmt.Fprintf(os.Stderr, "    reset   %s\n", strings.Join(c.Run, " "))
		}
	}
	fmt.Fprintln(os.Stderr, "\nEvery proof resets what it injected, including on failure and on interrupt.")
	fmt.Fprintln(os.Stderr, "Point this at a testbed. Never at anything you care about.")
	if acknowledged {
		fmt.Fprintln(os.Stderr, "\n--yes-inject-faults given, proceeding.")
		return true
	}
	fmt.Fprintln(os.Stderr, "\nRefusing to run without --yes-inject-faults.")
	return false
}

func writeReport(dir, entry, doc string, logger *log.Logger) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Printf("cannot write reports: %v", err)
		return
	}
	path := dir + "/" + entry + ".md"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		logger.Printf("cannot write %s: %v", path, err)
		return
	}
	logger.Printf("  report written to %s", path)
}

// listCoverage is the honest inventory: which entries have an end-to-end
// proof and which are still only asserted. An entry with no proof is not
// necessarily broken, but nothing has shown that it works.
func listCoverage(scenarios []catalog.Scenario, specs []proof.Spec) {
	have := map[string]string{}
	for _, sp := range specs {
		have[sp.Entry] = sp.Summary
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ENTRY\tPROOF\tFAULT")
	proven := 0
	for _, s := range scenarios {
		if summary, ok := have[s.ID]; ok {
			fmt.Fprintf(w, "%s\tyes\t%s\n", s.ID, summary)
			proven++
			continue
		}
		fmt.Fprintf(w, "%s\tNONE\t-\n", s.ID)
	}
	w.Flush()
	fmt.Printf("\n%d of %d entries have an end-to-end proof\n", proven, len(scenarios))
	if proven < len(scenarios) {
		fmt.Printf("%d entries are asserted but not demonstrated: nothing has shown that they fire on a real fault\n",
			len(scenarios)-proven)
	}
}

func summarize(results []proof.Result, scenarios []catalog.Scenario, specs []proof.Spec) int {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ENTRY\tRESULT\tFIRED AFTER\tNAMED\tRESOLVED\tDETAIL")
	failed := 0
	for _, r := range results {
		verdict := "pass"
		if !r.Passed {
			verdict = "FAIL"
			failed++
		}
		firedAfter := "-"
		if r.Fired {
			firedAfter = r.FiredAfter.Round(time.Second).String()
		}
		resolved := "-"
		switch {
		case r.Resolved:
			resolved = r.ResolveAfter.Round(time.Second).String()
		case r.Fired:
			resolved = "not checked"
		}
		detail := strings.Join(r.Failures, "; ")
		if detail == "" {
			detail = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\t%s\n",
			r.Entry, verdict, firedAfter,
			len(r.Named), len(r.Named)+len(r.Missing), resolved, detail)
	}
	w.Flush()

	fmt.Printf("\n%d proofs run, %d passed, %d failed\n", len(results), len(results)-failed, failed)
	if unproven := len(scenarios) - len(specs); unproven > 0 {
		fmt.Printf("%d catalog entries still have no proof at all\n", unproven)
	}
	if failed > 0 {
		return 1
	}
	return 0
}
