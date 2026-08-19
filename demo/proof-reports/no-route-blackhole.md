## Incident: Requests hitting no route (404 NR)

Scenario `no-route-blackhole` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **3.971** (threshold > 0.5 for 60s) since 2026-08-19T15:19:19Z.

### Diagnosis

Callers in the namespace are getting 404s with the NR (no route) flag: their requests match no VirtualService route, so the mesh refuses them before any destination is chosen. The usual cause is a routing edit that narrowed a route (a match condition live traffic no longer satisfies), removed the default route, or renamed a host. Because the request never resolves a destination, this is attributed to the source, not the destination: the report names which caller is being black-holed and lists the namespace's VirtualServices with their match and route, so the broken one is visible. No patch is proposed: the correct route depends on the intended routing table, which only the operator knows; regenerating it blind would replace a considered config with a guess.

### Evidence

| query | value |
| --- | --- |
| no-route-by-source-workload{source_workload="loadgen"} | 2.11 |

### Configuration evidence

- virtualservices-in-namespace (`VirtualService demo/*`) `items[*].metadata.name`: `payments`
- virtualservices-in-namespace (`VirtualService demo/*`) `items[*].spec.hosts`: `["payments"]`
- virtualservices-in-namespace (`VirtualService demo/*`) `items[*].spec.http[*].match`: `[{"uri":{"prefix":"/a-path-nothing-calls"}}]`
- virtualservices-in-namespace (`VirtualService demo/*`) `items[*].spec.http[*].route`: `[{"destination":{"host":"payments","subset":"v1"},"weight":80},{"destination":{"host":"payments","subset":"v2"},"weight":20}]`

### Proposed patch (VirtualService)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Find the VirtualService in the evidence whose match no longer covers the live traffic, or whose default route was removed, and restore the intended route. The source-workload breakdown names the caller being black-holed, and the recent change to that VirtualService is the cause.

This scenario requires human approval; nothing was applied to the cluster.
