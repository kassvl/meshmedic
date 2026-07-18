# MeshMedic

Deterministic first responder for service-mesh incidents: it detects in
seconds, opens an evidence-dossier pull request, and does it with zero LLM
and zero cluster mutation.

![MeshMedic demo: chaos to merged PR to healed mesh](demo/video/meshmedic-demo.gif)

*Every frame above is real: a live kind + Istio ambient mesh, a real latency
fault, the pull request MeshMedic opened with evidence from Prometheus, a
human merge, and Argo CD syncing the fix. Recording script in `demo/video/`.*

MeshMedic watches Istio's Prometheus telemetry, matches a human-reviewed
catalog of failure scenarios, and answers with a GitOps pull request that
carries the diagnosis, labeled evidence, configuration reads, and a rollback
plan. When the failure sits outside the catalog, a triage layer assembles a
dossier instead: which callers went silent, which workloads are logging
known failure signatures, and what rolled out recently with the exact
template diff.

Its scores against LLM-agentic and analyzer-based tools are published, with
the methodology and the reproducible scenarios, in
[mesh-incidents-bench](https://github.com/kassvl/mesh-incidents-bench).

## The gap it fills

[k8sgpt](https://github.com/k8sgpt-ai/k8sgpt) tells you what is wrong at the
object layer. [Flagger](https://github.com/fluxcd/flagger) protects
deployments while you ship. LLM-agentic SRE tools reason about open-ended
incidents. None of them speak the mesh's telemetry: traffic weights, outlier
ejection, retry policies, mTLS modes, waypoints, and, in ambient mode, the
L4 denials that never reach request metrics. On the benchmark's mesh
scenarios, object-state analyzers score zero because the objects stay
healthy while the telemetry degrades, and the agentic tool matched on the
scenarios it finished but exhausted its step budget on others. MeshMedic
owns the moment a running mesh breaks and answers with mesh-native fixes in
seconds.

```
Prometheus signal --> catalog match --> rendered Istio patch --> pull request --> human merges --> mesh heals
```

## Design rules

1. **Deterministic remediation.** Every fix comes from a reviewed catalog
   entry with an explicit signal, guardrails, and a rollback story. A language
   model may write the incident narrative in the PR body. It never writes the
   patch. Improvised YAML during an outage is how incidents get worse.
2. **Pull requests, not kubectl.** MeshMedic needs no write access to the
   cluster. The fix lands as a PR in your config repo with PromQL evidence
   attached, your existing policy checks (OPA, CI) run against it, and a human
   merges it. The audit trail is the git history you already have.

## Catalog

| Scenario | Failure | Mesh-native fix |
| --- | --- | --- |
| `canary-latency-rollback` | canary subset p99 regression | VirtualService weights back to stable |
| `error-surge-outlier-ejection` | sustained 5xx from bad endpoints | DestinationRule outlier detection |
| `retry-storm-damping` | retries amplifying an outage | cut retry attempts, hard route timeout |
| `connection-pool-overflow` | UO flags, circuit breaker shedding load | raise pool limits, with resource evidence |
| `mtls-policy-conflict` | plaintext clients hit strict mTLS (L7) | scoped PERMISSIVE fallback, flagged temporary |
| `mtls-policy-conflict-ambient` | plaintext client denied at L4 by ztunnel | scoped PERMISSIVE fallback, from TCP telemetry |
| `upstream-host-ejection-flood` | UH flags, mesh refuses ready endpoints | cap ejection, set minHealthPercent |
| `waypoint-overload-scale` | ambient waypoint saturated | scale the waypoint deployment |
| `traffic-vanished-triage` | traffic to a service stopped (client-side) | report-only dossier, no patch |

Every entry ships with the PromQL that detects it, evidence queries for the
PR body, guardrails, and a rollback note. Signals are labeled, so the report
names the offending workload rather than collapsing to one number, and can
attach configuration reads (a bad env var, a policy mode) and, for triage
entries, log signatures and rollout diffs. Entries are plain YAML in
[`catalog/`](catalog/); adding one is a PR, not a code change. No entry
merges without injecting the fault on the testbed and confirming the signal
is real.

### Detection where request metrics go blind

Two catalog entries exist because the usual signals are silent:

- `mtls-policy-conflict-ambient` reads ztunnel's L4 telemetry
  (`istio_tcp_connections_closed_total` with `response_flags="DENY"`), the
  denials that never become L7 requests in ambient mode. See the
  [reference doc](https://github.com/kassvl/mesh-incidents-bench/blob/main/docs/ambient-l4-denial-telemetry.md).
- `traffic-vanished-triage` fires on the *absence* of traffic that used to
  flow, then attaches the client's own failure logs and the most recent
  rollout diff. A bad client deploy stops the traffic and shows up nowhere
  in mesh request metrics; the root cause is usually one line of that diff.

## Try it

```console
$ go run ./cmd/meshmedic validate
ID                            SEVERITY  TARGET              TITLE
canary-latency-rollback       critical  VirtualService      Canary subset latency regression
...
catalog OK: 9 scenarios

$ go run ./cmd/meshmedic render --scenario canary-latency-rollback \
    --set service=payments --set namespace=prod \
    --set subset=v2 --set stable_subset=v1
apiVersion: networking.istio.io/v1
kind: VirtualService
...
```

`validate` reports 9 scenarios today.

Point the detector at a Prometheus and it evaluates every catalog signal for
the targets you configure, holding each breach for the scenario's `for`
duration before it fires. When one fires, it prints the incident report the
future PR opener will use as the pull request body: diagnosis, evidence
table, the rendered patch, and the rollback note.

```console
$ go run ./cmd/meshmedic watch --config examples/watch.yaml
meshmedic: watching 9 scenarios for 1 targets against http://localhost:9090 every 30s
```

Add a `gitops` section to the config and set `MESHMEDIC_GITHUB_TOKEN` (or
`GITHUB_TOKEN`), and firing turns into a pull request instead of only a
report: a branch named after the episode, one commit with the patch file,
and the incident report as the PR body.

```yaml
gitops:
  repo: you/your-config-repo
  path: istio/{{.namespace}}/{{.scenario}}.yaml
```

```console
meshmedic: canary-latency-rollback: opened https://github.com/you/your-config-repo/pull/1
```

## Prior art and limits

The closest prior art is [Robusta](https://github.com/robusta-dev/robusta)'s
deterministic playbooks, which enrich alerts with pod logs and change
tracking. Robusta proved that deterministic enrichment works; MeshMedic
makes it mesh-native (it reads the mesh's own telemetry, including ambient
L4) and PR-native (its output is a reviewable GitOps change, not a chat
message). k8sgpt and LLM-agentic tools are complementary rather than
competing: the first reads object state, the second reasons about
open-ended incidents; MeshMedic reads the telemetry layer between them.

The limits are deliberate and worth stating plainly. The catalog and triage
signatures cover frequent, signature-bearing failure classes: mesh
misconfigurations, bad client deploys, DNS and connection and TLS errors,
and configuration accidents. Novel failure modes with no signature,
cross-service causal chains, and problems that never emit a signal are out
of reach by design. Signatures need curation, which is why they live in a
reviewable catalog. An optional LLM layer may one day narrate a finished
dossier, but it will never drive detection, and MeshMedic is meant to stay
complete without it.

## Roadmap

See [ROADMAP.md](ROADMAP.md). The first milestone was a 60 second video of a
mesh healing itself through a merged pull request; the second was the
[reproducible mesh incident benchmark](https://github.com/kassvl/mesh-incidents-bench).
Both are done. Current work extends the deterministic taxonomy and adds a
baseline-memory layer so thresholds adapt to a cluster's own normal.

## License

Apache-2.0
