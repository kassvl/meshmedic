## Incident: Service slow because a dependency is slow

Scenario `upstream-dependency-latency` (severity warning) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **498.9** (threshold > 200 for 90s) since 2026-08-19T04:54:46Z.

### Diagnosis

The watched service is slow, but the latency is not its own: its calls to a downstream dependency are taking too long, and it is waiting on them. The report names the slow dependency from the service's outbound telemetry, so the operator looks one hop downstream instead of profiling a service that is only blocked. This is the latency counterpart to a dependency's errors: a caller sees the front service as slow, but the time is spent waiting on something it calls. No patch is proposed on the watched service: the fix belongs to the named dependency, or to a timeout or circuit breaker on the call to it, which is a decision for the operator once the dependency is identified.

### Evidence

| query | value |
| --- | --- |
| dependency-latency-p99-ms{destination_service_name="ledger"} | 494.7 |

### Proposed patch (Deployment)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Look at the dependency named in dependency-latency-p99-ms; the watched service is only blocked waiting on it. If the dependency cannot be sped up, a route timeout or a DestinationRule circuit breaker on the call to it bounds the wait, but that is a mitigation the operator chooses once the slow dependency is known.

This scenario requires human approval; nothing was applied to the cluster.
