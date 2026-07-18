## Incident: Service latency above its learned baseline

Scenario `latency-regression-vs-baseline` (severity warning) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **488** (threshold > 140.8 (3x the learned baseline) for 90s) since 2026-07-18T05:04:09Z.

### Diagnosis

A service's p99 request latency has climbed to a multiple of its own learned normal. This catches regressions that are abnormal for this cluster but sit below any fixed SLO number: a service that normally answers in 50 ms and now takes 300 ms is in trouble, yet a static 1000 ms threshold would miss it entirely. The threshold here is relative: once the baseline has warmed up on healthy traffic, the signal fires at baselineMultiplier times the learned normal. Until then the static threshold applies, so a cold start never fires on noise. No patch is proposed: a latency regression's cause is usually a recent rollout or resource pressure, which the operator investigates from the evidence and the rollout history, not an improvised config change.

### Evidence

| query | value |
| --- | --- |
| p99-by-version-ms{destination_version="v2"} | 492.8 |
| p99-by-version-ms{destination_version="v1"} | 46.53 |
| request-rate-per-s | 3.448 |

### Proposed patch (Deployment)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Find the workload named in p99-by-version-ms and check what rolled out to it recently; a bad image, a lowered CPU limit, or an injected latency knob is the usual cause. If the higher latency is legitimate and permanent (a genuine change in the workload), raise the baseline by letting it relearn, or set an explicit static threshold on this entry for the service.
