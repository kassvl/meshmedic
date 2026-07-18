# Baseline-relative thresholds: live run

Most detectors compare a signal against a fixed number. That misses the class
of regression that matters most in a healthy service: latency that is abnormal
*for this cluster* but still under any round-number SLO. A service that
normally answers in 48 ms and now takes 480 ms is in trouble, yet a static
1000 ms threshold sees nothing wrong.

The `latency-regression-vs-baseline` catalog entry is the first to use a
relative threshold. It learns the service's own p99 baseline from healthy
traffic, then fires at `baselineMultiplier` times that learned normal. Until
the baseline has warmed up it falls back to the static threshold, so a cold
start never fires on noise. This is the "knows your cluster's normal" moat
made concrete: an LLM agent invoked per incident has no memory of last week's
latency to compare against.

This is a captured run on the demo testbed (kind + Istio ambient 1.24.1,
`payments` service). The [F9 recorder](../f9-recorder/) ran in the same
process to show the two features compose.

## What was measured

| phase | observation |
| --- | --- |
| Warm-up | Baseline learned p99 = **47.2 ms** from 24 healthy samples (static 1000 ms threshold applied meanwhile, so nothing fired) |
| Inject | A 250 ms latency regression on `payments-v2` drove service p99 to **488 ms** |
| Fire | The scenario fired against a **140.8 ms** threshold (3x the learned normal), not the static 1000 ms. A fixed-threshold detector would have stayed silent |
| Evidence | The labeled breakdown named the culprit: `v2` at **492.8 ms** while `v1` held at **46.5 ms**. The report points at the regressed subset, not just a service-wide number |
| F9 | The recorder stayed **empty**: the catalog now explains this deviation, so there is nothing unmatched to record |

The threshold line in the fired report is the point:

> The signal has held at **488** (threshold > 140.8 (3x the learned baseline)
> for 90s)

The full report is in [`fired-report.md`](fired-report.md).

## How the two features compose

The F9 recorder records only deviations the catalog cannot explain. Once
`latency-regression-vs-baseline` exists, a latency deviation on `payments` is
explained, so the recorder correctly defers and writes nothing. Before the
entry existed, the same deviation would have been recorded as a fingerprint
for review (see the [F9 run](../f9-recorder/)). Learn, record the unexplained,
a human turns a real fingerprint into a catalog entry, and from then on the
catalog fires on it directly. That is the loop.

## Reproduce

```
meshmedic watch --config demo/f9-recorder/watch.yaml --catalog catalog
# once the baseline has warmed up (about 20 healthy samples), inject a
# regression that stays under the static threshold:
kubectl -n demo set env deploy/payments-v2 TIMING_50_PERCENTILE=250ms
# after the 90s for-duration the scenario fires with a relative threshold; reset:
kubectl -n demo set env deploy/payments-v2 TIMING_50_PERCENTILE=20ms
```
