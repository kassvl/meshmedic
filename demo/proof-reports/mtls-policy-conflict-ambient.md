## Incident: Plaintext client denied at L4 by ztunnel (ambient strict mTLS)

Scenario `mtls-policy-conflict-ambient` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **0.9809** (threshold > 0.2 for 60s) since 2026-08-19T14:43:23Z.

### Diagnosis

A client without mesh identity keeps opening plaintext connections to a service in a STRICT-mTLS ambient namespace. ztunnel rejects each attempt at L4, so nothing ever reaches the L7 request metrics: request-based monitoring stays green while the client fails every call. The signal lives one layer down, in ztunnel's TCP telemetry: connections closed with response_flags=DENY for the destination service. The reviewable de-escalation is a scoped PERMISSIVE PeerAuthentication on the service while the client gets enrolled into the mesh. This is deliberately a temporary security downgrade; the PR body says so and links the revert.

### Evidence

| query | value |
| --- | --- |
| denied-connections-by-source{source_principal="unknown", source_workload="plain-client", source_workload_namespace="default"} | 0.4803 |
| denied-connections-by-source{source_principal="unknown", source_workload="unknown", source_workload_namespace="unknown"} | 0 |
| connection-security-breakdown{connection_security_policy="mutual_tls"} | 7.045 |
| connection-security-breakdown{connection_security_policy="unknown"} | 0.4803 |

### Configuration evidence

- peer-authentication-policies (`PeerAuthentication demo/*`) `items[*].metadata.name`: `demo-strict`
- peer-authentication-policies (`PeerAuthentication demo/*`) `items[*].spec.mtls.mode`: `STRICT`

### Proposed patch (PeerAuthentication)

```yaml
apiVersion: security.istio.io/v1
kind: PeerAuthentication
metadata:
  name: payments-mtls-fallback
  namespace: demo
spec:
  selector:
    matchLabels:
      app: payments
  mtls:
    mode: PERMISSIVE
```

### Rollback

Enroll the denied client into the mesh (ambient label on its namespace or a sidecar) and restore STRICT. The denied-connections-by-source evidence names exactly which workloads still lack mesh identity. PERMISSIVE left in place indefinitely defeats the point of mesh mTLS.

This scenario requires human approval; nothing was applied to the cluster.
