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
	"github.com/kassvl/meshmedic/pkg/remediate"
	"github.com/kassvl/meshmedic/pkg/report"
)

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
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  meshmedic validate [--catalog dir]
  meshmedic render --scenario id --set key=value [--set ...] [--catalog dir]
  meshmedic watch --config watch.yaml [--catalog dir]`)
}

func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	dir := fs.String("catalog", "catalog", "catalog directory")
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
	dir := fs.String("catalog", "catalog", "catalog directory")
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
	dir := fs.String("catalog", "catalog", "catalog directory")
	cfgPath := fs.String("config", "watch.yaml", "watch config file")
	fs.Parse(args)

	scenarios, err := catalog.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog invalid:", err)
		os.Exit(1)
	}
	cfg, err := detect.LoadConfig(*cfgPath)
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
	}

	logger.Printf("watching %d scenarios for %d targets against %s every %s",
		len(scenarios), len(cfg.Targets), cfg.Prometheus, cfg.IntervalDuration())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d.Run(ctx, cfg.IntervalDuration())
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
