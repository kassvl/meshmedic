## Incident: Users getting 5xx at the ingress gateway

Scenario `ingress-edge-outage` (severity critical) fired for `ingress_workload=ingress-istio` `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **4.295** (threshold > 0.5 for 60s) since 2026-08-19T15:04:39Z.

### Diagnosis

The ingress gateway is returning 5xx to the traffic entering through it, so users cannot reach the app at all. This is a front-door outage, distinct from a single service erroring inside the mesh: the request fails at or before the gateway's backend, so no amount of internal health matters if users cannot get in. The usual causes are a broken HTTPRoute (a backend pointing at a wrong port or a service that does not exist), a backend with no healthy endpoints, or a listener or certificate problem on the gateway. The report lists the HTTPRoutes bound to the gateway with their backends, so the broken route is visible. No patch is proposed: the fix depends on which of those causes it is, and regenerating the routing blind would risk replacing a considered edge configuration with a guess.

### Evidence

| query | value |
| --- | --- |
| edge-errors-by-code{response_code="500"} | 2.2 |
| edge-errors-by-code{response_code="503"} | 0 |

### Configuration evidence

- httproutes-in-namespace (`HTTPRoute demo/*`) `items[*].metadata.name`: `payments-ingress`
- httproutes-in-namespace (`HTTPRoute demo/*`) `items[*].spec.hostnames`: `["payments.example.com"]`
- httproutes-in-namespace (`HTTPRoute demo/*`) `items[*].spec.rules[*].backendRefs`: `{"group":"","kind":"Service","name":"payments","port":9999,"weight":1}`

### Proposed patch (HTTPRoute)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Check the HTTPRoutes in the evidence: a backend pointing at the wrong port or a service that does not exist is the common cause, and edge-errors-by-code tells 503 (no healthy backend) from 500 (backend refused the connection). If the routes are correct, look at the gateway's listeners and certificates. Restore the intended backend or fix the gateway configuration.

This scenario requires human approval; nothing was applied to the cluster.
