## Incident: Sustained 5xx surge from a service

Scenario `error-surge-outlier-ejection` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **0.1976** (threshold > 0.15 for 120s) since 2026-08-19T05:11:35Z.

### Diagnosis

A service's 5xx ratio holds above 15 percent. When the errors come from a subset of bad endpoints (bad node, wedged pod), ejecting unhealthy hosts from the load balancing pool restores service without touching the workload. Outlier detection does that automatically once configured.

### Evidence

| query | value |
| --- | --- |
| errors-by-workload{destination_workload="payments-v2"} | 0.781 |
| errors-by-workload{destination_workload="payments-v1"} | 0 |
| requests-by-workload{destination_workload="payments-v1"} | 3.171 |
| requests-by-workload{destination_workload="payments-v2"} | 0.781 |

### Proposed patch (DestinationRule)

```yaml
apiVersion: networking.istio.io/v1
kind: DestinationRule
metadata:
  name: payments-outlier
  namespace: demo
spec:
  host: payments
  trafficPolicy:
    outlierDetection:
      consecutive5xxErrors: 5
      interval: 30s
      baseEjectionTime: 60s
      maxEjectionPercent: 50
```

### Rollback

If the 5xx ratio does not drop after ejection, the failure is uniform across endpoints (bad deploy, downstream dependency) and this patch is not the fix; revert it and check the canary-latency-rollback and upstream dependencies. maxEjectionPercent 50 caps the blast radius either way.

This scenario requires human approval; nothing was applied to the cluster.
