## Incident: No healthy upstream hosts left

Scenario `upstream-host-ejection-flood` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **8.543** (threshold > 1 for 60s) since 2026-08-19T15:11:59Z.

### Diagnosis

Requests fail with UH (no healthy upstream). This flag has more than one cause and they need different fixes, so read the evidence before acting. Either an over-aggressive outlier policy ejected the hosts (the patch below relaxes the ejection ceiling), or a DestinationRule subset selector matches no pods while the pods themselves are healthy (a routing mismatch the ejection patch would not fix; correct the subset labels instead). The destination-rule evidence shows the subsets and the outlier policy so the reviewer can tell which case this is: zero active ejections plus a subset whose labels match nothing points at a selector mismatch, not ejection.

### Evidence

| query | value |
| --- | --- |
| ejected-hosts | unavailable (query returned no samples) |
| ready-endpoints | 0 |

### Configuration evidence

- destination-rule (`DestinationRule demo/payments`) `spec.subsets`: `{"labels":{"version":"v1"},"name":"v1"} {"labels":{"version":"v2"},"name":"v2"}`
- destination-rule (`DestinationRule demo/payments`) `spec.trafficPolicy.outlierDetection`: `unavailable (spec.trafficPolicy.outlierDetection: no match)`

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
      consecutive5xxErrors: 10
      interval: 30s
      baseEjectionTime: 30s
      maxEjectionPercent: 25
      minHealthPercent: 50
```

### Rollback

If ready-endpoints evidence shows zero, the workload itself is down and no mesh patch helps; this entry only applies when Kubernetes reports ready endpoints that the mesh refuses to use. Restore the previous ejection policy after the incident review.

This scenario requires human approval; nothing was applied to the cluster.
