## Incident: Requests dropped by connection pool limits

Scenario `connection-pool-overflow` (severity warning) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **16.71** (threshold > 1 for 60s) since 2026-08-19T16:01:21Z.

### Diagnosis

Requests are failing with the UO (upstream overflow) response flag, meaning Envoy's circuit breaker is shedding load because the configured connection pool for the destination is exhausted. If the destination has spare capacity (CPU and memory healthy), the limits are simply undersized for current traffic and can be raised. If the destination is saturated, raising limits makes things worse; the evidence queries make that call reviewable.

### Evidence

| query | value |
| --- | --- |
| uo-rate-by-workload{destination_workload="payments-v1"} | 9.353 |
| uo-rate-by-workload{destination_workload="unknown"} | 5.876 |
| uo-rate-by-workload{destination_workload="payments-v2"} | 1.476 |
| request-rate-by-workload{destination_workload="payments-v1"} | 12.4 |
| request-rate-by-workload{destination_workload="unknown"} | 5.876 |
| request-rate-by-workload{destination_workload="payments-v2"} | 3.829 |

### Configuration evidence

- destination-resources (`Deployment demo/payments-v2`) `spec.replicas`: `1`
- destination-resources (`Deployment demo/payments-v2`) `spec.template.spec.containers[*].resources`: `{}`

### Proposed patch (DestinationRule)

```yaml
apiVersion: networking.istio.io/v1
kind: DestinationRule
metadata:
  name: payments-pool
  namespace: demo
spec:
  host: payments
  trafficPolicy:
    connectionPool:
      tcp:
        maxConnections: 512
      http:
        http1MaxPendingRequests: 256
        http2MaxRequests: 512
```

### Rollback

If UO drops but the destination starts failing under real load, the pool limit was doing its job; revert this patch and scale the destination workload instead. The reviewer should weigh request-rate-by-workload against the destination-resources configuration evidence before merging.

This scenario requires human approval; nothing was applied to the cluster.
