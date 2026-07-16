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

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
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
	handler := func(_ context.Context, inc detect.Incident) {
		patch, err := remediate.Render(inc.Scenario, inc.Params)
		if err != nil {
			logger.Printf("%s: rendering patch: %v", inc.Scenario.ID, err)
			patch = "# patch rendering failed, see logs\n"
		}
		fmt.Println(report.Markdown(inc, patch))
	}

	d := detect.New(scenarios, cfg.Targets, prom.NewClient(cfg.Prometheus), handler)
	d.Log = logger.Printf

	logger.Printf("watching %d scenarios for %d targets against %s every %s",
		len(scenarios), len(cfg.Targets), cfg.Prometheus, cfg.IntervalDuration())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d.Run(ctx, cfg.IntervalDuration())
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
