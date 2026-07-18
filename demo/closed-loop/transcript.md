## Incident: Service latency above its learned baseline

Scenario `latency-regression-vs-baseline` (severity warning) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **486.3** (threshold > 144.3 (3x the learned baseline) for 90s) since 2026-07-18T05:28:07Z.

### Diagnosis

A service's p99 request latency has climbed to a multiple of its own learned normal. This catches regressions that are abnormal for this cluster but sit below any fixed SLO number: a service that normally answers in 50 ms and now takes 300 ms is in trouble, yet a static 1000 ms threshold would miss it entirely. The threshold here is relative: once the baseline has warmed up on healthy traffic, the signal fires at baselineMultiplier times the learned normal. Until then the static threshold applies, so a cold start never fires on noise. No patch is proposed: a latency regression's cause is usually a recent rollout or resource pressure, which the operator investigates from the evidence and the rollout history, not an improvised config change.

### Evidence

| query | value |
| --- | --- |
| p99-by-version-ms{destination_version="v2"} | 492.5 |
| p99-by-version-ms{destination_version="v1"} | 47.56 |
| request-rate-per-s | 3.571 |

### Proposed patch (Deployment)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

*(report-only, so no patch; rollback guidance omitted here)*

... 3 minutes later, traffic reset, the p99 falls back under the learned threshold ...

## Resolved: Service latency above its learned baseline

Scenario `latency-regression-vs-baseline` for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2` has recovered.

The signal is back to **47.51** (threshold > 144.3). Open from 2026-07-18T05:28:07Z to 2026-07-18T05:31:22Z, a duration of 3m15s.
