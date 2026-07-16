// Command meshmedic is the CLI entry point. Two subcommands work today:
//
//	validate  load the catalog and list what the engine knows how to fix
//	render    fill a scenario's patch template with incident parameters
//
// The controller loop (Prometheus watch, PR automation) lands behind these.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/remediate"
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
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  meshmedic validate [--catalog dir]
  meshmedic render --scenario id --set key=value [--set ...] [--catalog dir]`)
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

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
