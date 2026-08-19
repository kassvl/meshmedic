## Incident: Route timeout shorter than backend latency

Scenario `route-timeout-too-short` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **4.095** (threshold > 0.5 for 60s) since 2026-08-19T05:38:17Z.

### Diagnosis

A VirtualService route timeout is shorter than the backend's real response time, so requests are cut off and returned as HTTP 504 with the UT (upstream request timeout) response flag while the backend is actually healthy and would have answered. The fix is to raise the route timeout above the backend's p99, or remove it, on the specific VirtualService. No patch is proposed automatically: the correct timeout depends on the service's real latency, and regenerating the VirtualService without the operator's existing routes and weights would risk replacing a considered routing table with an improvised one. Instead this entry pins the offending field: the VirtualService and its current timeout, next to the measured backend latency, so the operator can set the right value.

### Evidence

| query | value |
| --- | --- |
| timed-out-rate | 2.083 |
| backend-p99-latency-ms | 165.5 |

### Proposed patch (VirtualService)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Raise the route timeout on the named VirtualService above the backend-p99-latency-ms shown in the evidence, or remove it to fall back to the mesh default, preserving the existing routes and weights. If the backend latency itself is the real problem, the timeout is doing its job and the fix belongs in the backend, not the route.

This scenario requires human approval; nothing was applied to the cluster.
