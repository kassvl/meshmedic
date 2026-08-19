package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"text/tabwriter"
	"time"

	"github.com/kassvl/meshmedic/pkg/calibrate"
	"github.com/kassvl/meshmedic/pkg/catalog"
	"github.com/kassvl/meshmedic/pkg/detect"
	"github.com/kassvl/meshmedic/pkg/prom"
)

// runCalibrate observes the whole catalog against a cluster the operator
// asserts is healthy, and reports how close each entry came to firing.
//
// This is the check that catches what nothing else here can. The lock proves
// an entry is the one that was reviewed. `validate --against-prometheus`
// proves its metrics exist. Neither says anything about whether its threshold
// is right for this cluster, and a threshold that is wrong produces a pull
// request for a problem nobody has.
func runCalibrate(args []string) {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	dir := fs.String("catalog", defaultCatalogDir(), "catalog directory (or $MESHMEDIC_CATALOG)")
	cfgPath := fs.String("config", "", "watch config file (targets come from here)")
	promURL := fs.String("prometheus", "", "Prometheus base URL; with --target this replaces the config file")
	window := fs.Duration("window", 10*time.Minute, "how long to observe the healthy cluster")
	interval := fs.Duration("interval", 15*time.Second, "seconds between samples")
	marginFactor := fs.Float64("margin", 2, "headroom below which a quiet entry is still called marginal")
	minSamples := fs.Int("min-samples", 5, "resolved samples required before an entry is judged")
	strict := fs.Bool("strict", false, "exit non-zero unless every applicable entry is calibrated")
	var targets multiFlag
	fs.Var(&targets, "target", "target params as key=value,key=value (repeatable)")
	fs.Parse(args)

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

	// The longest hold duration in the catalog bounds how short a useful
	// window can be: an entry cannot be shown to hold past its threshold for
	// 120s inside a 60s window, and reporting it as calibrated would be a
	// guess dressed as a measurement.
	if longest := longestHold(scenarios); *window < 3*longest {
		fmt.Fprintf(os.Stderr,
			"window %s is too short: the catalog's longest hold duration is %s, so observe at least %s or an entry cannot be shown to hold\n",
			*window, longest, 3*longest)
		os.Exit(2)
	}

	calTargets := make([]calibrate.Target, 0, len(cfg.Targets))
	for _, t := range cfg.Targets {
		calTargets = append(calTargets, calibrate.Target{Params: t.Params})
	}

	logger := log.New(os.Stderr, "meshmedic: ", log.LstdFlags)
	runner := calibrate.New(calibrate.Config{
		Window:       *window,
		Interval:     *interval,
		MarginFactor: *marginFactor,
		MinSamples:   *minSamples,
	}, scenarios, calTargets, prom.NewClient(cfg.Prometheus))
	runner.Log = logger.Printf

	logger.Printf("observing %d entries across %d targets for %s, sampling every %s",
		len(scenarios), len(calTargets), *window, *interval)
	logger.Printf("the cluster must be healthy for this to mean anything: no injected faults, no ongoing incident")

	ctx, stop := signalContext()
	defer stop()

	deadline := time.Now().Add(*window)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	samples := 0
	for time.Now().Before(deadline) {
		runner.Sample(ctx, time.Now())
		samples++
		if samples%10 == 0 {
			logger.Printf("%d samples taken, %s remaining", samples, time.Until(deadline).Round(time.Second))
		}
		select {
		case <-ctx.Done():
			logger.Printf("interrupted after %d samples; reporting what was measured", samples)
			deadline = time.Now()
		case <-ticker.C:
		}
	}

	report := runner.Report()
	printCalibration(report)
	summary := calibrate.Summarize(report)

	fmt.Printf("\n%d calibrated, %d marginal, %d miscalibrated, %d unmeasured, %d not applicable\n",
		summary.Calibrated, summary.Marginal, summary.Miscalibrated, summary.Unmeasured, summary.NotApplicable)

	if summary.Passed() {
		fmt.Println("\nEvery applicable entry stayed quiet with room to spare on this cluster.")
		return
	}
	explainFailures(summary)
	if *strict {
		os.Exit(1)
	}
}

func printCalibration(report []calibrate.Observation) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTARGET\tVERDICT\tPEAK\tTHRESHOLD\tHEADROOM\tNOTE")
	for _, o := range report {
		peak := "-"
		headroom := "-"
		if o.Samples > 0 {
			switch o.Comparison {
			case "<", "<=":
				peak = fmt.Sprintf("%.4g", o.Min)
			default:
				peak = fmt.Sprintf("%.4g", o.Max)
			}
			switch {
			case math.IsInf(o.Headroom, 1):
				headroom = "inf"
			case math.IsNaN(o.Headroom):
				headroom = "-"
			default:
				headroom = fmt.Sprintf("%.3gx", o.Headroom)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s %.4g\t%s\t%s\n",
			o.Scenario, describeTarget(o.Params), o.Verdict, peak,
			o.Comparison, o.Threshold, headroom, o.Note)
	}
	w.Flush()
}

// explainFailures says what to do about each failing category, because a
// verdict an operator cannot act on is just a complaint.
func explainFailures(s calibrate.Summary) {
	fmt.Println()
	if s.Miscalibrated > 0 {
		fmt.Printf("%d entries fire on a healthy cluster. Each one is a pull request for a problem nobody has.\n"+
			"  Raise the threshold to sit above this cluster's healthy peak with room to spare, or\n"+
			"  give the entry a baselineMultiplier so it fires on deviation from this cluster's own normal\n"+
			"  instead of a fixed number that is wrong for every cluster but one.\n", s.Miscalibrated)
	}
	if s.Marginal > 0 {
		fmt.Printf("%d entries stayed quiet but are close enough that an ordinary busy day fires them.\n"+
			"  Silence today is not calibration. Widen the gap or make the threshold relative.\n", s.Marginal)
	}
	if s.Unmeasured > 0 {
		fmt.Printf("%d entries produced no readings, so their silence proves nothing about them.\n"+
			"  Run `meshmedic validate --against-prometheus` to find out whether the metric is missing,\n"+
			"  the selector matches nothing, or this target simply has no traffic of that kind.\n", s.Unmeasured)
	}
}

func longestHold(scenarios []catalog.Scenario) time.Duration {
	var longest time.Duration
	for _, s := range scenarios {
		if s.Signal.For == "" {
			continue
		}
		if d, err := time.ParseDuration(s.Signal.For); err == nil && d > longest {
			longest = d
		}
	}
	return longest
}

func signalContext() (context.Context, func()) {
	return context.WithCancel(context.Background())
}
