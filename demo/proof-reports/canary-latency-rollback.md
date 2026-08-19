## Incident: Canary subset latency regression

Scenario `canary-latency-rollback` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **2485** (threshold > 1000 for 90s) since 2026-08-19T05:32:00Z.

### Diagnosis

A newly shifted traffic subset (canary) shows p99 latency far above the stable subset. The fastest safe move during an incident is to send traffic back to the stable subset and investigate the canary offline. This entry assumes the DestinationRule defines the failing subset and a stable subset.

### Evidence

| query | value |
| --- | --- |
| p99-stable-for-comparison | 215.5 |
| canary-request-share | 0.2389 |

### Proposed patch (VirtualService)

```yaml
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: payments
  namespace: demo
spec:
  hosts:
    - payments
  http:
    - route:
        - destination:
            host: payments
            subset: v1
          weight: 100
        - destination:
            host: payments
            subset: v2
          weight: 0
```

### Rollback

Restore the previous VirtualService weights once the canary's p99 is back under the threshold in a staging replay, or roll forward with a fixed canary image. The PR that shifted traffic contains the prior weights in its diff.

This scenario requires human approval; nothing was applied to the cluster.
