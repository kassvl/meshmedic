# MeshMedic

Incident-time remediation for Istio. When the mesh degrades, MeshMedic turns
the Prometheus signal into a reviewable GitOps pull request that fixes it.

![MeshMedic demo: chaos to merged PR to healed mesh](demo/video/meshmedic-demo.gif)

*Every frame above is real: a live kind + Istio ambient mesh, a real latency
fault, the pull request MeshMedic opened with evidence from Prometheus, a
human merge, and Argo CD syncing the fix. Recording script in `demo/video/`.*

**Status: early development.** The core loop works end to end today: the
detector evaluates catalog signals against Prometheus, and when a scenario
fires, MeshMedic opens a pull request in your config repository with the
rendered patch and the evidence (see "Try it"). What remains for the first
milestone is the reproducible demo environment and the video on top of it.

## The gap it fills

[k8sgpt](https://github.com/k8sgpt-ai/k8sgpt) tells you what is wrong.
[Flagger](https://github.com/fluxcd/flagger) protects deployments while you
ship. General AI SRE agents restart pods and scale deployments, but none of
them speak the mesh's language: traffic weights, outlier ejection, retry
policies, mTLS modes, waypoints. MeshMedic owns the moment a running mesh
breaks, and answers with mesh-native fixes.

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
| `connection-pool-overflow` | UO flags, circuit breaker shedding load | raise pool limits, with CPU evidence |
| `mtls-policy-conflict` | plaintext clients hit strict mTLS | scoped PERMISSIVE fallback, flagged temporary |
| `upstream-host-ejection-flood` | UH flags, mesh refuses ready endpoints | cap ejection, set minHealthPercent |
| `waypoint-overload-scale` | ambient waypoint saturated | scale the waypoint deployment |

Every entry ships with the PromQL that detects it, evidence queries for the
PR body, guardrails, and a rollback note. Entries are plain YAML in
[`catalog/`](catalog/); adding one is a PR, not a code change.

## Try it

```console
$ go run ./cmd/meshmedic validate
ID                            SEVERITY  TARGET              TITLE
canary-latency-rollback       critical  VirtualService      Canary subset latency regression
...
catalog OK: 7 scenarios

$ go run ./cmd/meshmedic render --scenario canary-latency-rollback \
    --set service=payments --set namespace=prod \
    --set subset=v2 --set stable_subset=v1
apiVersion: networking.istio.io/v1
kind: VirtualService
...
```

Point the detector at a Prometheus and it evaluates every catalog signal for
the targets you configure, holding each breach for the scenario's `for`
duration before it fires. When one fires, it prints the incident report the
future PR opener will use as the pull request body: diagnosis, evidence
table, the rendered patch, and the rollback note.

```console
$ go run ./cmd/meshmedic watch --config examples/watch.yaml
meshmedic: watching 7 scenarios for 1 targets against http://localhost:9090 every 30s
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

## Roadmap

See [ROADMAP.md](ROADMAP.md). Short version: the first milestone is not a
feature list, it is a 60 second video of a mesh healing itself through a
merged pull request. The second is a reproducible mesh incident benchmark.

## License

Apache-2.0
