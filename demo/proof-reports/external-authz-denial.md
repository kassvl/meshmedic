## Incident: Traffic denied by an external authorization service (403/UAEX)

Scenario `external-authz-denial` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **4.257** (threshold > 0.5 for 60s) since 2026-08-19T14:57:08Z.

### Diagnosis

Requests to the service are being rejected with HTTP 403 and the UAEX (unauthorized external service) response flag: an external authorization service, wired in through an ext_authz filter, is denying the traffic. This is distinct from a native Istio AuthorizationPolicy, which stamps DENY: the decision here is made outside the mesh, so the fix is at the external authz service and its filter, not an Istio policy. The important failure mode is not a deliberate deny but an authz service that is misconfigured or down: when the authz dependency fails closed, every request becomes a 403 and the whole service is dark, even though the service itself is healthy. The report names who is being denied and lists the EnvoyFilters, where the ext_authz wiring lives. No patch is proposed: whether to fix the authz service, its rules, or the filter's failure mode is the operator's call.

### Evidence

| query | value |
| --- | --- |
| denied-by-source{source_workload="ingress-istio"} | 2.231 |

### Configuration evidence

- envoyfilters-in-namespace (`EnvoyFilter demo/*`) `items[*].metadata.name`: `payments-ext-authz`
- envoyfilters-in-namespace (`EnvoyFilter demo/*`) `items[*].spec.configPatches[*].applyTo`: `HTTP_FILTER`

### Proposed patch (EnvoyFilter)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Find the EnvoyFilter in the evidence that wires the ext_authz service and check whether that service is healthy and its rules are correct. If the authz service is down, restore it or, if the policy intent allows, switch the filter to fail-open so a dead authz service stops turning into a total outage. If the denials are deliberate, the caller is the one to fix.

This scenario requires human approval; nothing was applied to the cluster.
