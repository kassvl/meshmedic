## Incident: Traffic throttled by a rate limit (429/RL)

Scenario `rate-limit-throttling` (severity warning) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **2.724** (threshold > 0.5 for 60s) since 2026-08-19T13:32:42Z.

### Diagnosis

Requests to the service are being rejected with HTTP 429 and the RL (rate limited) response flag: a rate limit is throttling live traffic. The RL flag is unambiguous, only a rate limit produces it, so this is never a guess about a generic 429 an application might return. Either the limit is set too low for the legitimate load, or the traffic is genuinely excessive and the limit is doing its job. The report shows how much traffic is being rejected and lists the EnvoyFilters in the namespace, where a local rate limit is configured, so the operator can find the token bucket. No patch is proposed: raising the limit and shedding the load are opposite fixes, and only the operator knows whether the traffic is legitimate.

### Evidence

| query | value |
| --- | --- |
| throttled-by-source{source_workload="ingress-istio"} | 1.375 |

### Configuration evidence

- envoyfilters-in-namespace (`EnvoyFilter demo/*`) `items[*].metadata.name`: `payments-local-ratelimit`
- envoyfilters-in-namespace (`EnvoyFilter demo/*`) `items[*].spec.configPatches[*].applyTo`: `HTTP_FILTER`

### Proposed patch (EnvoyFilter)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. Find the EnvoyFilter in the evidence that configures the local rate limit and check its token bucket against the legitimate request rate. If the limit is too low, raise it; if the traffic is a genuine flood, the limit is working and the fix is upstream (caching, a backend scale-up, or a client fix), not a higher limit.

This scenario requires human approval; nothing was applied to the cluster.
