# Retired entries

An entry is removed only when it has been shown that it cannot do its job, and
the showing is recorded here. Deleting a detection silently reduces coverage
and leaves nobody able to tell a deliberate retirement from an accident, so
the measurement that justified it outlives the entry.

---

## `mtls-policy-conflict`, retired 2026-08-19

**What it claimed.** Plaintext clients rejected by strict mTLS at L7, detected
as `istio_requests_total` carrying `response_code="503"` with
`response_flags=~"UF|URX"`. It was the sidecar-mode counterpart to
`mtls-policy-conflict-ambient`.

**Why it went.** The signal it reads does not exist. Measured on a live kind +
Istio 1.24.1 testbed with the setup verified rather than assumed: STRICT mTLS
applied to a sidecar-mode namespace, workload pods injected and running 2/2, an
unenrolled plaintext caller genuinely rejected (`curl` exit 56, connection
reset), and an in-mesh caller in the same namespace holding 200s at 7.7 rps
throughout, so the mesh was healthy and enforcing.

The mesh recorded nothing:

| queried | result |
| --- | --- |
| `istio_requests_total{destination_workload="orders-v2", response_code="503"}` | no series |
| `istio_tcp_connections_closed_total{destination_service_namespace="demo-sidecar"}` | no series |

A sidecar drops the connection at the TLS layer before any metric exists to
stamp it. Cross-checked against every recorded run in this repository and in
[mesh-incidents-bench](https://github.com/kassvl/mesh-incidents-bench): the only
entry that has ever fired for this failure class is
`mtls-policy-conflict-ambient`. This one never fired anywhere, on any run, at
any point.

**What the measurement is worth.** It sharpens the ambient entry's claim rather
than weakening the catalog. The difference between the data planes here is not
that one signal is better shaped than the other; it is that ambient's ztunnel
emits `istio_tcp_connections_closed_total{response_flags="DENY"}` for this
denial and a sidecar emits nothing at all. Any tool watching a sidecar mesh for
this class is waiting for something that will never arrive. The full write-up
is in the benchmark's
[ambient L4 denial telemetry reference](https://github.com/kassvl/mesh-incidents-bench/blob/main/docs/ambient-l4-denial-telemetry.md).

**What would bring it back.** A signal built on telemetry a sidecar actually
emits for a rejected handshake. Nothing in the current Istio metric surface
offers one, which is why this is a retirement rather than a rewrite.
