# F9 unmatched-incident recorder: live run

The recorder baselines a set of generic anomaly signals per target and appends
a fingerprint when one deviates from its learned normal while no catalog
scenario is active for that target. It records only. A fingerprint is raw
material for a human to review and, if real, turn into a validated catalog
entry; nothing here fires an incident or a patch.

This is a captured run on the demo testbed (kind + Istio ambient 1.24.1,
`payments` service, v1 stable + v2 canary). It demonstrates the case the
recorder exists for: a regression that is abnormal for this cluster but too
small to trip any fixed catalog threshold.

## What was measured

The anomaly signal is the service-wide p99 request latency for `payments`
([`watch.yaml`](watch.yaml)). Interval 5s so the baseline warms up quickly.

| phase | observation |
| --- | --- |
| Warm-up | Baseline learned p99 = **48.0 ms** from 24 healthy samples (live p99 47.5 ms) |
| Inject | A 250 ms latency regression on `payments-v2` drove service p99 to **477.9 ms** |
| Catalog | **Silent**: 477.9 ms is above 3x the learned normal but below `canary-latency`'s fixed 1000 ms threshold, so no scenario fired |
| Recorder | Wrote **13 fingerprints**, deviation factor 6.66x to 9.99x, each with signal, value, baseline, and factor |
| Guardrail | The baseline stayed frozen at **47.87 ms** across all 13 records: only non-deviating values feed it, so the anomaly never became the new normal |

The last row is the point. A recorder that let the anomaly move the baseline
would forget the incident by the next tick and, worse, normalize the
regression. Here the learned normal held while the deviation was recorded for
review.

## A recorded fingerprint

```json
{"time":"2026-07-18T04:34:37Z","target":{"namespace":"demo","service":"payments","subset":"v2","workload":"payments-v2"},"signal":"payments-service-p99-latency","value":318.75,"baseline":47.87,"factor":6.66}
```

A sample of the run is in [`unmatched-sample.jsonl`](unmatched-sample.jsonl).

## Reproduce

Point the config at a Prometheus with mesh telemetry, then inject a latency
regression on the watched service that stays under the catalog's fixed
thresholds:

```
meshmedic watch --config demo/f9-recorder/watch.yaml --catalog catalog
# in another shell, once the baseline has warmed up:
kubectl -n demo set env deploy/payments-v2 TIMING_50_PERCENTILE=250ms
# watch the unmatched log fill while the catalog stays quiet, then reset:
kubectl -n demo set env deploy/payments-v2 TIMING_50_PERCENTILE=20ms
```

The recorder needs `baselineState`, `anomalyWatch`, and `unmatchedLog` all set;
without a baseline it has no normal to deviate from and stays disabled.
