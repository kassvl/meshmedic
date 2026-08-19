// Command meshmedic-prove runs the end-to-end evidence for catalog entries:
// it injects each entry's fault into a live cluster, watches a real detector,
// and asserts that the entry fired, named the culprit, kept its neighbours
// quiet, and cleared on reset.
//
// It is a separate binary from meshmedic on purpose. The tool's central claim
// is that it holds no cluster write credentials and mutates nothing; a
// subcommand that runs `kubectl apply` would undermine that at the level of
// perception even though the detection path is untouched. This binary is
// excluded from the release archives and the container image. It exists to be
// run by a maintainer against a testbed, never in production.
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

	logger := log.New(os.Stderr, "prove: ", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := proof.NewRunner(scenarios, prom.NewClient(*promURL))

	results := make([]proof.Result, 0, len(specs))
	for i, sp := range specs {
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
