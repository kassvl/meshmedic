## Incident: Fault-injection VirtualService left in production

Scenario `fault-injection-left-in-production` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **2.076** (threshold > 0.5 for 60s) since 2026-08-19T07:21:27Z.

### Diagnosis

A VirtualService is injecting faults (aborts or delays) into live traffic: requests fail with the FI response flag, usually a leftover from a chaos test or a staging config that reached production. The FI flag is the tell. Unlike a genuine 5xx surge, the endpoints are healthy and the failures are synthetic, so outlier ejection would evict good hosts for no reason and a backend fix would find nothing wrong. No patch is proposed automatically: removing a fault stanza cleanly needs the VirtualService's real routes, and regenerating it would risk replacing a considered routing table. Instead this entry pins the offending VirtualService and its fault configuration so the operator can remove exactly that stanza.

### Evidence

| query | value |
| --- | --- |
| fault-injected-rate | 1.011 |

### Configuration evidence

- virtualservice-fault (`VirtualService demo/payments`) `spec.http[*].fault`: `{"abort":{"httpStatus":503,"percentage":{"value":50}}}`

### Proposed patch (VirtualService)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Remove the fault stanza from the named VirtualService, preserving the existing routes and weights. If the fault injection is intentional (an active chaos experiment), this is working as designed and the alert is the thing to silence, not the config.

This scenario requires human approval; nothing was applied to the cluster.
