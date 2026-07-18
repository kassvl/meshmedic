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
template diff. When the incident recovers, it closes the loop with a
resolution report and the time the incident was open.

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
owns the moment a running mesh breaks and answers in seconds: a mesh-native
patch where the fix is mechanical and safe, and an evidence dossier that names
the root cause where it is not.

```
Prometheus signal --> catalog match --> rendered Istio patch --> pull request --> human merges --> mesh heals
```

## Design rules

1. **Deterministic by commitment.** Every fix comes from a reviewed catalog
   entry with an explicit signal, guardrails, and a rollback story. There is
   no LLM in the detection or remediation path, and that is the identity, not
   a limitation to grow out of: the moment a model writes the applied patch,
   the guarantees below (zero cost, reproducibility, safety) are gone.
   Improvised YAML during an outage is how incidents get worse.
2. **Pull requests, not kubectl.** MeshMedic needs no write access to the
   cluster. The fix lands as a PR in your config repo with PromQL evidence
   attached, your existing policy checks (OPA, CI) run against it, and a human
   merges it. The audit trail is the git history you already have.

## Catalog

Nineteen entries today, each with the PromQL that detects it, evidence
queries for the report, guardrails, and a rollback note. Entries that carry a
mesh-native patch propose it; entries where the right fix depends on intent
are `report-only` and deliver an evidence dossier instead of a guess.

| Scenario | Failure | Response |
| --- | --- | --- |
| `canary-latency-rollback` | canary subset p99 regression | VirtualService weights back to stable |
| `latency-regression-vs-baseline` | p99 above the service's own learned normal | report-only, relative threshold |
| `error-surge-outlier-ejection` | sustained 5xx from bad endpoints | DestinationRule outlier detection |
| `upstream-dependency-errors` | service failing because a dependency it calls is failing | report-only, names the culprit dependency |
| `upstream-dependency-latency` | service slow because a dependency it calls is slow | report-only, names the slow dependency |
| `retry-storm-damping` | retries amplifying an outage | cut retry attempts, hard route timeout |
| `connection-pool-overflow` | UO flags, circuit breaker shedding load | raise pool limits, with resource evidence |
| `route-timeout-too-short` | 504/UT, timeout shorter than backend latency | report-only |
| `no-route-blackhole` | 404/NR, requests match no route | report-only, source-keyed |
| `ingress-edge-outage` | users getting 5xx at the ingress gateway (front-door outage) | report-only, lists the HTTPRoutes |
| `upstream-host-ejection-flood` | UH flags, mesh refuses ready endpoints | cap ejection, set minHealthPercent |
| `mtls-policy-conflict` | plaintext clients hit strict mTLS (L7) | scoped PERMISSIVE fallback, flagged temporary |
| `mtls-policy-conflict-ambient` | plaintext client denied at L4 by ztunnel | scoped PERMISSIVE fallback, from TCP telemetry |
| `authz-deny-flood` | AuthorizationPolicy denying live traffic (403) | report-only |
| `rate-limit-throttling` | traffic rejected with 429/RL by a rate limit | report-only, lists the EnvoyFilters |
| `external-authz-denial` | traffic denied by an external authz service (403/UAEX) | report-only, lists the EnvoyFilters |
| `fault-injection-left-in-production` | a fault-injection rule left enabled (FI) | report-only |
| `waypoint-overload-scale` | ambient waypoint saturated | scale the waypoint deployment |
| `traffic-vanished-triage` | traffic to a service stopped (client-side) | report-only dossier |

Signals are labeled, so the report names the offending workload rather than
collapsing to one number, and can attach configuration reads (a bad env var,
a policy mode) and, for triage entries, log signatures and rollout diffs.
Entries are plain YAML in [`catalog/`](catalog/); adding one is a PR, not a
code change. No entry merges without injecting the fault on the testbed and
confirming the signal is real, a discipline that has repeatedly caught what
the specification does not tell you: that a no-route request is stamped
`destination_service_name=unknown` (so `no-route-blackhole` keys on the
source), that a wrong-port client logs "Empty reply from server" rather than
"connection refused", and that a rolled-back Deployment reuses its
ReplicaSet, defeating age-based rollout detection.

## Beyond the fixed catalog

Three engine capabilities extend detection past a static list of thresholds,
each unit-tested and validated live on the testbed (captured runs in
[`demo/`](demo/)):

- **Baseline memory** ([`pkg/baseline`](pkg/baseline)): a scenario can fire on
  a deviation from a target's own learned normal instead of a fixed number.
  `latency-regression-vs-baseline` catches a latency regression that is
  abnormal for this cluster yet below any fixed SLO. A warm-up guardrail keeps
  a cold start from firing on noise, and only healthy values feed the
  baseline, so an ongoing incident cannot drift the normal upward and silence
  itself ([`demo/baseline-relative`](demo/baseline-relative)).
- **Closed loop** (resolution reports): when a firing incident's signal falls
  back under its threshold, MeshMedic prints a resolution with the interval
  the incident was open. Only a genuine recovery resolves; a breach that
  never fired, or one whose traffic simply vanished, produces no false
  all-clear ([`demo/closed-loop`](demo/closed-loop)).
- **Unmatched-incident recorder** ([`pkg/recorder`](pkg/recorder)): a
  deviation from the learned baseline that no catalog entry explains is
  written to a log as a fingerprint for a human to review and, if real, turn
  into a validated entry. It records only. No learned signature ever drives
  remediation without human review and testbed validation
  ([`demo/f9-recorder`](demo/f9-recorder)).

## Detection where request metrics go blind

Some entries exist because the usual signals are silent:

- `mtls-policy-conflict-ambient` reads ztunnel's L4 telemetry
  (`istio_tcp_connections_closed_total` with `response_flags="DENY"`), the
  denials that never become L7 requests in ambient mode. See the
  [reference doc](https://github.com/kassvl/mesh-incidents-bench/blob/main/docs/ambient-l4-denial-telemetry.md).
- `traffic-vanished-triage` fires on the *absence* of traffic that used to
  flow, then attaches the client's own failure logs and the most recent
  rollout diff. A bad client deploy stops the traffic and shows up nowhere
  in mesh request metrics; the root cause is usually one line of that diff.
- `no-route-blackhole` fires when requests match no route (404/NR). Because
  a no-route request never resolves a destination, it is attributed to the
  source: the report names the black-holed caller and lists the namespace's
  VirtualServices, so the over-narrow or removed route is visible.

## Try it

```console
$ go run ./cmd/meshmedic validate
ID                            SEVERITY  TARGET              TITLE
canary-latency-rollback       critical  VirtualService      Canary subset latency regression
...
catalog OK: 19 scenarios
```

Point the detector at a Prometheus and it evaluates every catalog signal for
the targets you configure, holding each breach for the scenario's `for`
duration before it fires. When one fires, it prints the incident report the
PR opener uses as the pull request body: diagnosis, evidence table, the
rendered patch, and the rollback note.

```console
$ go run ./cmd/meshmedic watch --config examples/watch.yaml
meshmedic: watching 19 scenarios for 1 targets against http://localhost:9090 every 30s
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

## Prior art and limits

The closest prior art is [Robusta](https://github.com/robusta-dev/robusta)'s
deterministic playbooks, which enrich alerts with pod logs and change
tracking. Robusta proved that deterministic enrichment works; MeshMedic
makes it mesh-native (it reads the mesh's own telemetry, including ambient
L4) and PR-native (its output is a reviewable GitOps change, not a chat
message). k8sgpt and LLM-agentic tools are complementary rather than
competing: the first reads object state, the second reasons about
open-ended incidents; MeshMedic reads the telemetry layer between them.

The limits are deliberate and worth stating plainly. Total coverage is
impossible for any tool: even the strongest LLM agents top out well short of
every scenario. So comprehensive coverage is not the goal. The goal is to
cover the common failure classes deterministically, degrade gracefully on
the tail (a triage dossier rather than nothing), and grow the catalog through
the recorder loop. The catalog and triage signatures cover frequent,
signature-bearing failure classes: mesh misconfigurations, bad client
deploys, DNS and connection and TLS errors, and configuration accidents.
Novel failure modes with no signature, cross-service causal chains, and
problems that never emit a signal are out of reach by design. Stating what
the tool cannot see is part of trusting what it can.

## Roadmap

See [ROADMAP.md](ROADMAP.md). The 60 second video of a mesh healing itself
through a merged pull request and the
[reproducible mesh incident benchmark](https://github.com/kassvl/mesh-incidents-bench)
are both done, as are the baseline-memory, closed-loop, and
unmatched-incident-recorder layers above. Current work extends the
deterministic taxonomy from the mesh's own failure vocabulary (Envoy response
flags) and hardens the benchmark's credibility with scenarios authored
independently of this tool.

## License

Apache-2.0
