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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"text/template"
	"time"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
	"github.com/kassvl/meshmedic/pkg/proof"
	"gopkg.in/yaml.v3"
)

func main() {
	// `doctor` is the only subcommand, and everything else keeps the flag-only
	// form v1.0.0 shipped with, so a documented command line does not stop
	// working. Anything starting with a dash is the legacy form.
	args := os.Args[1:]
	doctorMode := len(args) > 0 && args[0] == "doctor"
	if doctorMode {
		args = args[1:]
	}

	name := "meshmedic-prove"
	if doctorMode {
		name = "meshmedic-prove doctor"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	catalogDir := fs.String("catalog", "catalog", "catalog directory")
	proofDir := fs.String("proofs", "proof", "proof spec directory")
	promURL := fs.String("prometheus", "http://127.0.0.1:9090", "Prometheus base URL")
	only := fs.String("entry", "", "prove one entry instead of all of them")
	list := fs.Bool("list", false, "list the entries that have a proof, and those that do not")
	outDir := fs.String("out", "", "write each incident report under this directory")
	yes := fs.Bool("yes-inject-faults", false, "acknowledge that this mutates the cluster it is pointed at")
	kubeContext := fs.String("kube-context", os.Getenv("MESHMEDIC_KUBE_CONTEXT"), "kubeconfig context to read through; must be the cluster the inject commands target")
	quiesce := fs.Duration("quiesce", 150*time.Second, "settle time between proofs, so one proof's fault does not leak into the next")
	// A suite outlives the things that were up when it started. Waiting for
	// the observer to come back is not leniency: nothing is injected while it
	// is gone, so the alternative to waiting is not a stricter measurement,
	// it is a verdict about an entry nobody watched.
	waitObserver := fs.Duration("wait-observer", 2*time.Minute, "before each proof, wait up to this long for Prometheus to answer instead of failing the entry")
	// Blind runs only. See proof.Retryable: retrying a real failure is how a
	// harness launders a bug into a pass.
	retries := fs.Int("retry-blind", 1, "re-run a proof this many times when the harness went blind; failures are never retried")
	resultsPath := fs.String("results", "", "write the run's results as JSON to this path")
	fs.Parse(args)

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

	// Coverage is a property of the repository, not of the subset this
	// invocation chose to run. Measuring it after the --entry filter made a
	// single-entry run announce that seventeen entries had no proof, when
	// sixteen of them did.
	allSpecs := len(specs)

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

	// Everything that has to be true before a run is worth starting, checked
	// and printed. A suite that begins without this can spend an hour
	// producing failures that say nothing: seventeen minutes of that happened
	// here when a port-forward died unnoticed.
	ok := preflight(*promURL, *kubeContext, scenarios, specs, logger)

	// `doctor` is this and nothing else: the same checks, no injection, an
	// exit code a script can branch on. It exists because these checks were
	// run by hand five times in one session before a suite was trusted to
	// start, and because the answer to "is this testbed fit to measure
	// anything" is worth having without mutating the testbed to find out.
	if doctorMode {
		if ok {
			logger.Printf("this testbed is fit to prove against; nothing was injected")
			return
		}
		logger.Printf("this testbed is not fit to prove against; nothing was injected")
		os.Exit(1)
	}
	if !ok {
		os.Exit(1)
	}

	// Print exactly what is about to be done to the cluster, then require an
	// acknowledgement. A tool that mutates a mesh should never do it because
	// someone pasted a command without reading it, and the list below is the
	// difference between an informed act and an accident.
	if !confirmed(specs, *yes) {
		os.Exit(2)
	}

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
	observer := prom.NewClient(*promURL)
	runner := proof.NewRunner(scenarios, observer, reader)

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

		// Ask whether anyone is watching before putting a fault in a cluster.
		// Injecting into an unobserved cluster produces a fault, a verdict,
		// and no information.
		if _, up := proof.WaitForObserver(ctx, observer, *waitObserver, 5*time.Second, nil, nil,
			func(f string, a ...any) { logger.Printf("  "+sp.Entry+": "+f, a...) }); !up {
			logger.Printf("[%d/%d] %s: SKIPPED, the observer never came back within %s; nothing was injected",
				i+1, len(specs), sp.Entry, *waitObserver)
			results = append(results, proof.Result{
				Entry:       sp.Entry,
				Blind:       true,
				BlindReason: "Prometheus did not answer within " + waitObserver.String(),
				Failures:    []string{"skipped: nothing was injected, so this says nothing about the entry"},
			})
			continue
		}

		res := runner.Run(ctx, sp)
		for attempt := 1; attempt <= *retries && proof.Retryable(res) && ctx.Err() == nil; attempt++ {
			logger.Printf("  %s: the harness went blind (%s); re-running, attempt %d of %d",
				sp.Entry, res.BlindReason, attempt, *retries)
			if _, up := proof.WaitForObserver(ctx, observer, *waitObserver, 5*time.Second, nil, nil,
				func(f string, a ...any) { logger.Printf("  "+sp.Entry+": "+f, a...) }); !up {
				break
			}
			res = runner.Run(ctx, sp)
		}
		results = append(results, res)

		verdict := "PASS"
		if !res.Passed {
			verdict = "FAIL"
		}
		if res.Blind {
			verdict = "BLIND"
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

	// A run whose results live only in a terminal has to be re-read by a human
	// to be used again. Eleven scenarios were scored by hand into a scratch
	// file on 2026-08-19 for exactly this reason.
	if *resultsPath != "" {
		writeResults(*resultsPath, results, logger)
	}

	os.Exit(summarize(results, len(scenarios), allSpecs))
}

// writeResults records the run in a form something other than a person can
// read. It is deliberately the Result struct as it stands rather than a
// prettier schema: a report that drifts from what the harness actually
// measured is worse than no report.
func writeResults(path string, results []proof.Result, logger *log.Logger) {
	body, err := json.MarshalIndent(struct {
		Results []proof.Result `json:"results"`
	}{results}, "", "  ")
	if err != nil {
		logger.Printf("cannot encode results: %v", err)
		return
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		logger.Printf("cannot write %s: %v", path, err)
		return
	}
	logger.Printf("results written to %s", path)
}

// preflight proves the run can produce a meaningful result before it starts.
// Each check answers "would a failure after this point mean anything?", and a
// no anywhere means the answer is no.
func preflight(promURL, kubeContext string, scenarios []catalog.Scenario, specs []proof.Spec, logger *log.Logger) bool {
	type check struct {
		name string
		run  func() (string, bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := prom.NewClient(promURL)

	kubectlArgs := func(args ...string) []string {
		if kubeContext != "" {
			return append([]string{"--context=" + kubeContext}, args...)
		}
		return args
	}
	run := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "kubectl", kubectlArgs(args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}

	checks := []check{
		{"prometheus answers", func() (string, bool) {
			if _, err := client.Query(ctx, "vector(1)"); err != nil {
				return promURL + ": " + err.Error() + " (is the port-forward up?)", false
			}
			return promURL, true
		}},
		{"prometheus has mesh telemetry", func() (string, bool) {
			v, err := client.Query(ctx, `count(count by (destination_service_name) (istio_requests_total))`)
			if err != nil {
				return "no istio_requests_total series: nothing this suite injects could be observed", false
			}
			return fmt.Sprintf("%.0f services reporting", v), true
		}},
		{"cluster reachable", func() (string, bool) {
			out, err := run("get", "nodes", "-o", "name")
			if err != nil {
				return "kubectl cannot reach the cluster: " + err.Error(), false
			}
			return strings.ReplaceAll(out, "\n", ", "), true
		}},
		{"kube context matches the inject commands", func() (string, bool) {
			// Every spec pins its own context in argv. Reading through a
			// different one means the dossiers describe another cluster,
			// which is how a triage proof came back full of connection
			// errors while the traffic was fine.
			pinned := map[string]bool{}
			for _, sp := range specs {
				for _, c := range sp.Inject {
					for _, a := range c.Run {
						if strings.HasPrefix(a, "--context=") {
							pinned[strings.TrimPrefix(a, "--context=")] = true
						}
					}
				}
			}
			if len(pinned) == 0 {
				return "specs pin no context; reads and writes may target different clusters", true
			}
			for c := range pinned {
				if c != kubeContext {
					return fmt.Sprintf("specs inject into %q but reads go to %q", c, kubeContext), false
				}
			}
			return kubeContext, true
		}},
		{"testbed is clean", func() (string, bool) {
			// A leftover fault from an interrupted run makes the next proof
			// measure someone else's incident.
			out, err := run("-n", "demo", "get", "authorizationpolicy,envoyfilter", "-o", "name")
			if err != nil {
				return "cannot check: " + err.Error(), false
			}
			if out != "" {
				return "leftover fault objects in demo: " + strings.ReplaceAll(out, "\n", ", "), false
			}
			return "no leftover fault objects", true
		}},
		{"no entry is already breaching", func() (string, bool) {
			// The general version of "is the testbed clean". Enumerating the
			// knobs a fault might have left behind is brittle and always one
			// fault-shape short; asking the catalog whether anything is
			// currently in breach catches a leftover of any shape, including
			// one nobody has written a proof for yet.
			breaching, unreadable := currentlyBreaching(ctx, scenarios, specs, client)
			if len(breaching) > 0 {
				return "already in breach before injection: " + strings.Join(breaching, ", "), false
			}
			// Silence from a server that is not answering is not quiet, and
			// saying so here would clear a testbed nobody managed to look at.
			if unreadable > 0 {
				return fmt.Sprintf("%d entries could not be read, so nothing can be said about whether the testbed is quiet", unreadable), false
			}
			return "catalog quiet on this testbed", true
		}},
		{"workloads ready", func() (string, bool) {
			out, err := run("-n", "demo", "get", "pods", "--no-headers")
			if err != nil {
				return "cannot list pods: " + err.Error(), false
			}
			total, ready := 0, 0
			for _, line := range strings.Split(out, "\n") {
				f := strings.Fields(line)
				if len(f) < 3 {
					continue
				}
				total++
				if f[2] == "Running" {
					ready++
				}
			}
			if total == 0 || ready != total {
				return fmt.Sprintf("%d of %d pods running in demo", ready, total), false
			}
			return fmt.Sprintf("%d/%d running", ready, total), true
		}},
	}

	logger.Printf("preflight: %d checks before anything is injected", len(checks))
	ok := true
	for _, c := range checks {
		detail, passed := c.run()
		mark := "ok  "
		if !passed {
			mark = "FAIL"
			ok = false
		}
		logger.Printf("  [%s] %-42s %s", mark, c.name, detail)
	}
	if !ok {
		logger.Printf("preflight failed: a run starting from here would produce failures that say nothing about the catalog")
	}
	return ok
}

// currentlyBreaching evaluates every entry once against the targets the
// proofs use and returns the ids already past their threshold. It is a single
// pass, so a hold duration is irrelevant: any breach at all before a fault is
// injected means the run would be measuring someone else's incident.
// currentlyBreaching reports which entries are already in breach, and how many
// could not be read at all.
//
// The second return value is the whole reason this signature is not just a
// slice. An empty query result and an unreachable server are not the same
// fact: for a failure counter, no series is the healthy reading, while a
// refused connection means nobody looked. Folding them together lets the
// preflight announce a quiet testbed on the strength of a dead Prometheus,
// which is precisely the blind-versus-clear confusion the detector's
// four-state model exists to prevent. A harness allowed to make the mistake
// it is checking for is worse than no harness.
func currentlyBreaching(ctx context.Context, scenarios []catalog.Scenario, specs []proof.Spec, client *prom.Client) (breaching []string, unreadable int) {
	seen := map[string]bool{}
	asked := map[string]bool{}
	for _, sp := range specs {
		for _, s := range scenarios {
			if seen[s.ID] {
				continue
			}
			query, err := renderForPreflight(s.ID, s.Signal.PromQL, sp.Target)
			if err != nil {
				continue // not applicable to this target
			}
			v, err := client.Query(ctx, query)
			switch {
			case errors.Is(err, prom.ErrNoData):
				continue // no series is the healthy reading for a failure counter
			case err != nil:
				if !asked[s.ID] {
					asked[s.ID] = true
					unreadable++
				}
				continue
			}
			if breachedPreflight(v, s.Signal.Comparison, s.Signal.Threshold) {
				seen[s.ID] = true
				breaching = append(breaching, fmt.Sprintf("%s (%.4g %s %.4g)", s.ID, v, s.Signal.Comparison, s.Signal.Threshold))
			}
		}
	}
	sort.Strings(breaching)
	return breaching, unreadable
}

func renderForPreflight(name, promql string, params map[string]string) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(promql)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, params); err != nil {
		return "", err
	}
	return b.String(), nil
}

func breachedPreflight(v float64, op string, threshold float64) bool {
	switch op {
	case ">":
		return v > threshold
	case "<":
		return v < threshold
	case ">=":
		return v >= threshold
	case "<=":
		return v <= threshold
	}
	return false
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
	declared := loadUnprovable()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ENTRY\tPROOF\tFAULT")
	proven := 0
	for _, s := range scenarios {
		if summary, ok := have[s.ID]; ok {
			fmt.Fprintf(w, "%s\tyes\t%s\n", s.ID, summary)
			proven++
			continue
		}
		if reason, ok := declared[s.ID]; ok {
			fmt.Fprintf(w, "%s\tunprovable\t%s\n", s.ID, firstSentence(reason))
			continue
		}
		fmt.Fprintf(w, "%s\tNONE\t-\n", s.ID)
	}
	w.Flush()

	silent := len(scenarios) - proven - len(declared)
	fmt.Printf("\n%d proven, %d declared unprovable with a reason, %d silently missing\n",
		proven, len(declared), silent)
	if silent > 0 {
		fmt.Println("A silently missing entry is the ambiguous kind: nobody can tell whether it has not been")
		fmt.Println("got to yet or cannot be done. Write a proof, or declare it in proof/UNPROVABLE.yaml.")
	}
}

// loadUnprovable reads the declarations of entries that cannot be proven here.
// A missing file means none are declared, which is a legitimate state and not
// an error: it simply means every gap is of the ambiguous kind.
func loadUnprovable() map[string]string {
	data, err := os.ReadFile("proof/UNPROVABLE.yaml")
	if err != nil {
		return map[string]string{}
	}
	var doc struct {
		Unprovable []struct {
			Entry  string `yaml:"entry"`
			Reason string `yaml:"reason"`
		} `yaml:"unprovable"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, u := range doc.Unprovable {
		out[u.Entry] = strings.TrimSpace(u.Reason)
	}
	return out
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	if len(s) > 110 {
		return s[:110] + "..."
	}
	return s
}

func summarize(results []proof.Result, entries, proofs int) int {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ENTRY\tRESULT\tFIRED AFTER\tNAMED\tRESOLVED\tDETAIL")
	failed, blind := 0, 0
	for _, r := range results {
		// Three outcomes, not two. A blind run and a failing run are both
		// "not a proof", but only one of them is about the entry, and
		// printing FAIL next to an entry nobody could observe is the exact
		// mistake Result.Blind was added to prevent. Neither counts as
		// proven, so both still end the command non-zero.
		verdict := "pass"
		switch {
		case r.Blind:
			verdict = "BLIND"
			blind++
		case !r.Passed:
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

	fmt.Printf("\n%d proofs run, %d passed, %d failed, %d blind\n",
		len(results), len(results)-failed-blind, failed, blind)
	if blind > 0 {
		fmt.Printf("%d run(s) said nothing about their entry: the harness could not see the cluster.\n"+
			"  Fix the observer and re-run those; do not read them as failures.\n", blind)
	}
	if unproven := entries - proofs; unproven > 0 {
		fmt.Printf("%d catalog entries still have no proof at all\n", unproven)
	}
	if failed > 0 || blind > 0 {
		return 1
	}
	return 0
}
