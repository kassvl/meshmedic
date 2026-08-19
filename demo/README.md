# The demo testbed, and what has been measured on it

A kind cluster running Istio 1.24.1 in ambient mode, with a `payments` service
(v1 stable, v2 canary) that calls a downstream `ledger` on every request,
fronted by a north-south ingress Gateway, plus a sidecar-mode `orders` service
in its own namespace so the catalog can be exercised in both data planes.

Everything below is a captured run against that testbed. The raw output sits
beside this file; this is the reading of it.

```console
$ ./scripts/00-cluster.sh          # kind cluster
$ ./scripts/01-istio.sh            # Istio ambient, Gateway API CRDs, Prometheus, kube-state-metrics
$ ./scripts/02-app.sh              # payments v1/v2, ledger, ingress, loadgen, sidecar orders
$ kubectl -n istio-system port-forward svc/prometheus 9090:9090 &
$ meshmedic watch --config watch.yaml
```

Add `./scripts/03-argocd.sh` with a `GITHUB_TOKEN` to close the loop through a
config repository, and `./scripts/04-grafana.sh` for the dashboard used in the
video.

---

## The sixty second video

`video/meshmedic-demo.mp4`, every frame from one real episode: chaos injected,
Prometheus fires, MeshMedic opens a pull request with the patch and the
evidence, a human merges it, the dashboards go green. The recording script and
its run log are in `video/`; the log carries the pull request it actually
opened.

## End-to-end proofs

[`proof-reports/`](proof-reports/) holds the incident report each catalog entry
produced when its fault was injected and a real detector watched. Sixteen
entries, each asserted to fire inside its own hold duration, name the culprit,
keep its declared neighbours quiet, and clear on reset. That directory's README
carries the table and the bugs the runs found in the harness itself.

## Calibration on a healthy cluster

[`calibration/live-run.txt`](calibration/) is eight minutes against the testbed
with nothing injected and three targets, one of them a namespace that does not
exist so the report has a known-unobservable control.

It found one real miscalibration: `canary-latency-rollback` peaked at 1450ms
against its 1000ms threshold on sidecar-mode `orders` and held there for its
full hold duration, on a cluster with nothing wrong. The threshold was chosen
against the ambient service; the sidecar one is simply slower, and nothing
before this check would have said so.

It also put a number on what `marginal` costs. Two later proof runs measured
`latency-regression-vs-baseline`'s learned normal independently: 169.9ms on one
and 208.4ms on the next, a 23 percent swing between two idle windows on the
same untouched cluster. That variance is the signal rather than noise in it,
because this service carries a whole network hop to `ledger` in its p99. Method
and how to read a verdict: [`docs/calibration.md`](../docs/calibration.md).

## Baseline-relative thresholds

Most detectors compare a signal against a fixed number, which misses the
regression that matters most in a healthy service: latency that is abnormal
*for this cluster* and still under any round-number SLO.

[`baseline-relative/`](baseline-relative/) is the first entry to use a relative
threshold, live. It fired when `payments` p99 hit 488ms against a 140.8ms
threshold, three times the 47ms it had learned as normal, a regression a static
1000ms number would not have seen. The labeled evidence named the regressed
subset: v2 at 493ms beside v1 at 47ms. A warm-up guardrail keeps a cold start
from firing on noise, and only healthy values feed the baseline, so an ongoing
incident cannot drift the normal upward and silence itself.

## Closing the loop

Detection is half a story; an operator also needs to know when the incident is
over and how long it lasted. [`closed-loop/`](closed-loop/) captures an
incident opening at 05:28:07Z and closing at 05:31:22Z with `resolved after
3m15s`, in the same terminal.

Only the firing-to-clear edge resolves. A breach that clears before its hold
duration never became an incident and produces nothing, and an incident whose
traffic simply vanishes resets without a resolution, because no traffic is not
the same as recovered.

## The unmatched-incident recorder

[`f9-recorder/`](f9-recorder/) baselines a set of generic anomaly signals per
target and appends a fingerprint when one deviates while no catalog entry
explains it. It records only: a fingerprint is raw material for a human to
review and, if real, turn into a validated entry.

The captured run is the case it exists for. A 250ms regression drove `payments`
p99 to 478ms against a learned normal of 48ms, too small to trip the 1000ms
fixed threshold, so the catalog stayed silent while the recorder logged 13
fingerprints. The baseline held frozen at 48ms throughout, which is the
guardrail working: the anomaly never became the new normal.

## Mode-agnostic detection

The catalog was authored against ambient, where L7 telemetry is reported by the
waypoint. Most entries query `reporter=~"destination|waypoint"` so they should
also work in classic sidecar mode. "Should" is not "does".

The proof rather than the assertion:
`error-surge-outlier-ejection` fired against the sidecar-injected `orders`
service at a 1.0 ratio over its 120s hold, with telemetry carrying
`reporter=destination`. It adds no catalog entry, because the same class in
another data plane would be padding.

The measured limit in the other direction is in
[`catalog/RETIRED.md`](../catalog/RETIRED.md): a plaintext connection rejected
by strict mTLS is recorded by ambient's ztunnel and by a sidecar not at all,
which is why the L7 entry for that class was retired.
