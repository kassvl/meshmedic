// Command meshmedic is the CLI entry point. Three subcommands work today:
//
//	validate  load the catalog and list what the engine knows how to fix
//	render    fill a scenario's patch template with incident parameters
//	watch     evaluate catalog signals against a live Prometheus and print
//	          an incident report with the proposed patch when one fires
//
// The PR opener lands behind these.
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

	"github.com/kassvl/meshmedic/pkg/baseline"
	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/gitops"
	"github.com/kassvl/meshmedic/pkg/kube"
	"github.com/kassvl/meshmedic/pkg/prom"
	"github.com/kassvl/meshmedic/pkg/recorder"
	"github.com/kassvl/meshmedic/pkg/remediate"
	"github.com/kassvl/meshmedic/pkg/report"
)

// version is stamped at build time (-ldflags "-X main.version=..."). The
// default matters: a report or a lock file written by an unversioned build
// should say so rather than claim a release it is not.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "validate":
		runValidate(os.Args[2:])
	case "render":
		runRender(os.Args[2:])
	case "watch":
		runWatch(os.Args[2:])
	case "check":
		runCheck(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("meshmedic", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  meshmedic validate [--catalog dir]
  meshmedic render --scenario id --set key=value [--set ...] [--catalog dir]
  meshmedic watch --config watch.yaml [--catalog dir]
  meshmedic watch --prometheus URL --target k=v,k=v [--target ...] [--interval 30s]

The second watch form needs no config file, which is the quickest way to point
MeshMedic at a Prometheus you already run:

  meshmedic watch --prometheus http://localhost:9090 \
      --target service=payments,namespace=demo,workload=payments-v2

The catalog directory defaults to ./catalog, or $MESHMEDIC_CATALOG when set.`)
}

// defaultCatalogDir resolves where the catalog lives when --catalog is not
// given. The environment variable is what lets a container image bake the
// catalog in at a fixed path and stay correct regardless of the working
// directory the user runs it from.
func defaultCatalogDir() string {
	if dir := os.Getenv("MESHMEDIC_CATALOG"); dir != "" {
		return dir
	}
	return "catalog"
}

func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	dir := fs.String("catalog", defaultCatalogDir(), "catalog directory (or $MESHMEDIC_CATALOG)")
	fs.Parse(args)

	scenarios, err := catalog.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog invalid:", err)
		os.Exit(1)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSEVERITY\tTARGET\tTITLE")
	for _, s := range scenarios {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ID, s.Severity, s.Remediation.Target.Kind, s.Title)
	}
	w.Flush()
	fmt.Printf("catalog OK: %d scenarios\n", len(scenarios))
}

func runRender(args []string) {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	dir := fs.String("catalog", defaultCatalogDir(), "catalog directory (or $MESHMEDIC_CATALOG)")
	id := fs.String("scenario", "", "scenario id")
	var sets multiFlag
	fs.Var(&sets, "set", "template parameter, key=value (repeatable)")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "--scenario is required")
		os.Exit(2)
	}
	params := map[string]string{}
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "bad --set %q, want key=value\n", kv)
			os.Exit(2)
		}
		params[k] = v
	}

	scenarios, err := catalog.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog invalid:", err)
		os.Exit(1)
	}
	for _, s := range scenarios {
		if s.ID != *id {
			continue
		}
		out, err := remediate.Render(s, params)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(out)
		return
	}
	fmt.Fprintf(os.Stderr, "unknown scenario %q, run `meshmedic validate` to list\n", *id)
	os.Exit(1)
}

func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	dir := fs.String("catalog", defaultCatalogDir(), "catalog directory (or $MESHMEDIC_CATALOG)")
	cfgPath := fs.String("config", "", "watch config file (default watch.yaml when --prometheus is not given)")
	promURL := fs.String("prometheus", "", "Prometheus base URL; with --target this replaces the config file")
	interval := fs.String("interval", "", "evaluation interval, e.g. 30s (flag form only)")
	var targets multiFlag
	fs.Var(&targets, "target", "target params as key=value,key=value (repeatable, flag form only)")
	fs.Parse(args)

	scenarios, err := catalog.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog invalid:", err)
		os.Exit(1)
	}

	// Two ways in: a config file for the full feature set, or flags for the
	// quickest possible first run against a Prometheus that already exists.
	var cfg detect.Config
	switch {
	case *promURL != "":
		if *cfgPath != "" {
			fmt.Fprintln(os.Stderr, "--config and --prometheus are alternatives; pass one")
			os.Exit(2)
		}
		cfg, err = detect.ConfigFromFlags(*promURL, *interval, targets)
	default:
		path := *cfgPath
		if path == "" {
			path = "watch.yaml"
		}
		if len(targets) > 0 || *interval != "" {
			fmt.Fprintln(os.Stderr, "--target and --interval belong to the --prometheus form; put them in the config file instead")
			os.Exit(2)
		}
		cfg, err = detect.LoadConfig(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "config invalid:", err)
		os.Exit(1)
	}

	logger := log.New(os.Stderr, "meshmedic: ", log.LstdFlags)

	var opener *gitops.Client
	if cfg.GitOps != nil {
		token := os.Getenv("MESHMEDIC_GITHUB_TOKEN")
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
		if token == "" {
			fmt.Fprintln(os.Stderr, "gitops is configured but neither MESHMEDIC_GITHUB_TOKEN nor GITHUB_TOKEN is set")
			os.Exit(1)
		}
		opener, err = gitops.NewClient("", cfg.GitOps.Repo, token)
		if err != nil {
			fmt.Fprintln(os.Stderr, "config invalid:", err)
			os.Exit(1)
		}
	}

	handler := func(ctx context.Context, inc detect.Incident) error {
		patch, err := remediate.Render(inc.Scenario, inc.Params)
		if err != nil {
			// A template or parameter problem does not fix itself on
			// retry; log it, keep the report, skip the pull request.
			logger.Printf("%s: rendering patch: %v", inc.Scenario.ID, err)
			fmt.Println(report.Markdown(inc, "# patch rendering failed, see logs\n"))
			return nil
		}
		doc := report.Markdown(inc, patch)
		fmt.Println(doc)
		if opener == nil {
			return nil
		}
		path, err := gitops.PathFor(cfg.GitOps.Path, inc.Params, inc.Scenario.ID)
		if err != nil {
			logger.Printf("%s: %v", inc.Scenario.ID, err)
			return nil
		}
		url, err := opener.Open(ctx, gitops.PullRequest{
			Branch:        gitops.BranchFor(inc.Scenario.ID, time.Now()),
			Base:          cfg.GitOps.Base,
			Title:         fmt.Sprintf("[meshmedic] %s: %s", inc.Scenario.Title, describeTarget(inc.Params)),
			Body:          doc,
			Path:          path,
			Content:       []byte(patch),
			CommitMessage: fmt.Sprintf("meshmedic: %s (%s)", inc.Scenario.Remediation.Action, inc.Scenario.ID),
		})
		if err != nil {
			// Transient outage on the far side: hand the episode back to
			// the detector so the PR is attempted again next tick.
			logger.Printf("%s: opening pull request: %v", inc.Scenario.ID, err)
			return err
		}
		logger.Printf("%s: opened %s", inc.Scenario.ID, url)
		return nil
	}

	d := detect.New(scenarios, cfg.Targets, prom.NewClient(cfg.Prometheus), handler)
	d.Log = logger.Printf
	d.CoverageProbe = cfg.CoverageProbe
	// Coverage every cycle, including the good ones. A number that only shows
	// up when it is bad is a number nobody learns to read, and the invariant
	// this enforces is that silence is only ever reported as an answer when
	// the tool can prove it was looking.
	d.OnCycle = func(c detect.Cycle) { logger.Print(c.Line()) }
	if cfg.StateFile != "" {
		d.StateFile = cfg.StateFile
		if err := d.LoadState(cfg.StateFile); err != nil {
			logger.Printf("state: load %s: %v (starting with no open incidents)", cfg.StateFile, err)
		} else {
			logger.Printf("incident state persisted at %s, so a restart does not re-open open incidents", cfg.StateFile)
		}
	} else {
		logger.Print("no stateFile configured: a restart will re-open incidents that are still breaching")
	}
	// Close the loop: when a firing incident recovers, print the resolution so
	// the operator watching the terminal sees the incident open and then clear
	// with its own duration.
	d.OnResolve = func(_ context.Context, res detect.Resolution) error {
		fmt.Println(report.Resolved(res))
		logger.Printf("%s: resolved after %s", res.Scenario.ID, res.Duration().Round(time.Second))
		return nil
	}
	if reader, err := kube.NewReader(); err != nil {
		logger.Printf("configuration and triage evidence disabled: %v", err)
	} else {
		d.Objects = reader
		d.Triage = reader
	}
	if cfg.BaselineState != "" {
		store := baseline.New(cfg.BaselineState, 0.05)
		if err := store.Load(); err != nil {
			logger.Printf("baseline: load %s: %v (starting fresh)", cfg.BaselineState, err)
		}
		d.Baseline = store
		logger.Printf("baseline-relative thresholds enabled, state at %s", cfg.BaselineState)

		if cfg.UnmatchedLog != "" && len(cfg.AnomalyWatch) > 0 {
			d.Recorder = recorder.New(cfg.UnmatchedLog)
			d.AnomalyWatch = cfg.AnomalyWatch
			logger.Printf("unmatched-incident recorder enabled, %d anomaly signals, log at %s",
				len(cfg.AnomalyWatch), cfg.UnmatchedLog)
		}
	} else if cfg.UnmatchedLog != "" {
		logger.Printf("unmatched-incident recorder needs baselineState set; skipping")
	}

	logger.Printf("watching %d scenarios for %d targets against %s every %s",
		len(scenarios), len(cfg.Targets), cfg.Prometheus, cfg.IntervalDuration())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d.Run(ctx, cfg.IntervalDuration())
}

// runCheck evaluates every target once and reports coverage, then exits
// non-zero if any target could not be observed. It is the CI and readiness
// form of the same invariant `watch` enforces continuously: a detector that
// cannot see its targets must not be mistaken for a quiet one.
func runCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	dir := fs.String("catalog", defaultCatalogDir(), "catalog directory (or $MESHMEDIC_CATALOG)")
	cfgPath := fs.String("config", "", "watch config file")
	promURL := fs.String("prometheus", "", "Prometheus base URL; with --target this replaces the config file")
	once := fs.Bool("once", true, "evaluate every target once and exit (the only mode today)")
	var targets multiFlag
	fs.Var(&targets, "target", "target params as key=value,key=value (repeatable)")
	fs.Parse(args)
	_ = *once

	scenarios, err := catalog.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog invalid:", err)
		os.Exit(1)
	}
	var cfg detect.Config
	if *promURL != "" {
		cfg, err = detect.ConfigFromFlags(*promURL, "", targets)
	} else {
		path := *cfgPath
		if path == "" {
			path = "watch.yaml"
		}
		cfg, err = detect.LoadConfig(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "config invalid:", err)
		os.Exit(1)
	}

	logger := log.New(os.Stderr, "meshmedic: ", 0)
	// A check must not act on what it finds: no incidents delivered, no
	// resolutions, no pull requests. It only measures whether the tool can
	// see, which is why the handler is a no-op.
	d := detect.New(scenarios, cfg.Targets, prom.NewClient(cfg.Prometheus),
		func(context.Context, detect.Incident) error { return nil })
	d.Log = logger.Printf
	d.CoverageProbe = cfg.CoverageProbe

	cycle := d.Tick(context.Background(), time.Now())

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TARGET\tCOVERAGE")
	unobserved := map[string]bool{}
	for _, params := range cycle.UnobservedTargets {
		unobserved[describeTarget(params)] = true
	}
	for _, t := range cfg.Targets {
		name := describeTarget(t.Params)
		status := "observed"
		if unobserved[name] {
			status = "UNOBSERVED"
		}
		fmt.Fprintf(w, "%s\t%s\n", name, status)
	}
	w.Flush()
	fmt.Println(cycle.Line())

	if !cycle.Healthy() {
		fmt.Fprintf(os.Stderr,
			"\n%d of %d targets could not be observed. Silence from this detector is not an answer for them.\n",
			cycle.Unobserved, len(cfg.Targets))
		os.Exit(1)
	}
}

// describeTarget names the incident's subject for the PR title, preferring
// service.namespace when both are present.
func describeTarget(params map[string]string) string {
	if params["service"] != "" && params["namespace"] != "" {
		return params["service"] + "." + params["namespace"]
	}
	if params["workload"] != "" && params["namespace"] != "" {
		return params["workload"] + "." + params["namespace"]
	}
	return "target"
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
