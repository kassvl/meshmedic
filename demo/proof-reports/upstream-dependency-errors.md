## Incident: Service failing because a dependency is failing

Scenario `upstream-dependency-errors` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **7.057** (threshold > 0.5 for 60s) since 2026-08-19T04:40:31Z.

### Diagnosis

The watched service is returning 5xx to its callers, but the cause is not the service itself: its own calls to a downstream dependency are failing, and it is propagating that failure upward. The report names the failing dependency from the service's outbound telemetry, so the operator fixes the dependency rather than the healthy service in front of it. This is the difference between a symptom and a root cause: a caller sees the front service erroring, but ejecting or rolling back that service would do nothing, because the fault is one hop downstream. No patch is proposed on the watched service: the fix belongs to the named dependency, and a mesh-native mitigation (circuit-break the dependency so calls fail fast) is a decision for the operator once the dependency is identified.

### Evidence

| query | value |
| --- | --- |
| failing-dependencies-by-service{destination_service_name="ledger"} | 3.708 |

### Proposed patch (Deployment)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Fix the dependency named in failing-dependencies-by-service; the watched service is healthy and only relaying the failure. If the dependency cannot be fixed immediately, a DestinationRule with outlier detection on that dependency lets calls fail fast instead of hanging, but that is a mitigation the operator chooses once the culprit is known.

This scenario requires human approval; nothing was applied to the cluster.
