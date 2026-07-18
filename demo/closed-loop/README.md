# Closed loop: incident opens, then resolves with MTTR

Detection is half a story. An operator also needs to know when the incident is
over and how long it lasted. MeshMedic closes the loop: when a firing
incident's signal falls back under its threshold, it prints a resolution
report carrying the interval the condition held, so the incident opens and
closes in the same terminal without diffing two dashboards.

Only the firing-to-clear edge resolves. A breach that clears before its
for-duration never became an incident, so it produces nothing. An incident
whose traffic vanishes (no data) resets without a resolution, because no
traffic is not the same as recovered.

This is a captured run on the demo testbed (kind + Istio ambient 1.24.1,
`payments`). It reuses the [baseline-relative](../baseline-relative/) latency
entry so the full lifecycle of a real scenario is shown.

## What was measured

| phase | observation |
| --- | --- |
| Fire | A 250 ms regression drove p99 to 486 ms; the scenario fired against the 144.3 ms baseline-relative threshold, opening at 05:28:07Z |
| Recover | Traffic was reset to healthy; the p99 fell back under the threshold at 05:31:22Z |
| Resolve | MeshMedic printed a resolution report: `resolved after 3m15s`, the exact interval the condition held |

The closing line from the run:

> The signal is back to **47.51** (threshold > 144.3). Open from
> 2026-07-18T05:28:07Z to 2026-07-18T05:31:22Z, a duration of 3m15s.

The full open-and-close transcript is in [`transcript.md`](transcript.md).

## Why the edges matter

A resolution report is only trustworthy if it never lies about recovery. Two
edges are deliberately silent:

- A breach that clears before the `for` duration never fired, so there is
  nothing to resolve. Unit-tested in `TestNoResolutionWhenIncidentNeverFired`.
- A firing incident whose signal goes to no-data resets without claiming
  recovery: the service could be scaled to zero or fully down, not fixed.
  Unit-tested in `TestNoResolutionWhenTrafficVanishes`.

The recovery path itself is `TestResolutionReportsOnRecovery`, which checks the
duration is measured from the breach start to the recovery tick.

## Reproduce

```
meshmedic watch --config demo/f9-recorder/watch.yaml --catalog catalog
# warm up, inject a regression, wait out the for-duration so it fires, then:
kubectl -n demo set env deploy/payments-v2 TIMING_50_PERCENTILE=20ms
# once the p99 falls back under the threshold, the resolution report prints.
```
