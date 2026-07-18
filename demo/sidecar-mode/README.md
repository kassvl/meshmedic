# Mode-agnostic detection: sidecar as well as ambient

The catalog was authored and validated against an Istio ambient testbed, where
L7 telemetry is reported by the waypoint (`reporter="waypoint"`). Most entries
query `reporter=~"destination|waypoint"` so they should also work in classic
sidecar mode, where the same telemetry is reported by the workload's own sidecar
(`reporter="destination"`). "Should" is not "does", so this is the proof rather
than the assertion.

The testbed grew a sidecar-mode counterpart ([`demo/manifests/sidecar-orders.yaml`](../manifests/sidecar-orders.yaml)):
an `orders` service (v1 stable, v2 canary) in the `demo-sidecar` namespace with
automatic sidecar injection. Every pod comes up 2/2 (workload plus istio-proxy),
and its request telemetry carries `reporter=destination` and `reporter=source`,
not `waypoint`.

## What was measured

A representative mode-agnostic entry, `error-surge-outlier-ejection` (signal
`reporter=~"destination|waypoint"`, 5xx ratio over 0.15), was run against the
sidecar service. Injecting `ERROR_RATE=0.9` on both subsets drove the ratio up,
and the entry fired:

| time after inject | 5xx ratio | fired |
| --- | --- | --- |
| 30s | 0.57 | no |
| 90s | 0.75 | no (holding the 120s for-duration) |
| 120s | 1.0 | yes |

The dossier:

> Scenario `error-surge-outlier-ejection` fired for `namespace=demo-sidecar`
> `service=orders`. The signal has held at **1** (threshold > 0.15 for 120s).

The `reporter=destination` telemetry matched the `reporter=~"destination|waypoint"`
query exactly as the ambient waypoint telemetry does, so the entry is genuinely
mode-agnostic rather than ambient-only.

## Honest scope

This validates that the mode-agnostic entries work in both data planes; it does
not add a catalog entry, because the failure is the same class in either mode
(a 5xx surge is a 5xx surge), and padding the count with a mode variant would be
dishonest. Entries that are inherently ambient stay ambient: `waypoint-overload-scale`
needs a waypoint, and `mtls-policy-conflict-ambient` reads ztunnel's L4
telemetry, neither of which exists in sidecar mode. Entries that query
`reporter="waypoint"` alone (for example `no-route-blackhole`, `authz-deny-flood`)
would need their own sidecar-mode validation before the reporter is widened,
because a sidecar may attribute the same event to a different reporter, and
guessing that without observing it is exactly the mistake this discipline avoids.
