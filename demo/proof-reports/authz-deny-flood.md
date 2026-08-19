## Incident: Legitimate callers denied by an authorization policy

Scenario `authz-deny-flood` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **4.314** (threshold > 0.5 for 60s) since 2026-08-19T12:41:23Z.

### Diagnosis

A workload's calls to a service are being rejected with HTTP 403 by an Istio AuthorizationPolicy, while the caller has a valid mesh identity (this is an authorization decision, not an mTLS handshake failure). The usual causes are a DENY rule that is broader than intended or a default-deny namespace missing an ALLOW rule for this caller. No patch is proposed automatically: whether the denial is a mistake or is working as intended is a judgment only an operator can make, and auto-loosening an authorization policy could open a real security hole. Instead this entry assembles a dossier: which caller is being denied, whether it has a valid mesh identity, and every AuthorizationPolicy in the namespace with its action and rules, so the reviewer can see at a glance which policy is denying and decide.

### Evidence

| query | value |
| --- | --- |
| denied-403-by-source{source_principal="spiffe://cluster.local/ns/demo/sa/default", source_workload="loadgen", source_workload_namespace="demo"} | 2.136 |

### Configuration evidence

- authorization-policies (`AuthorizationPolicy demo/*`) `items[*].metadata.name`: `payments-block-loadgen`
- authorization-policies (`AuthorizationPolicy demo/*`) `items[*].spec.action`: `DENY`
- authorization-policies (`AuthorizationPolicy demo/*`) `items[*].spec.rules`: `[{"from":[{"source":{"principals":["cluster.local/ns/demo/sa/default"]}}]}]`

### Proposed patch (AuthorizationPolicy)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. If the denial is a mistake, the fix is usually a scoped ALLOW for the named caller or a correction to the too-broad DENY rule; the authorization-policies evidence names the policy to change. If the denial is intended, the caller is the thing to fix, not the mesh.

This scenario requires human approval; nothing was applied to the cluster.
