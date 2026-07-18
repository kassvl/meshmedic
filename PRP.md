# MeshMedic - Product Requirement Prompt (PRP)

Single source of truth for what MeshMedic is, what has been measured, what
is claimed, and what happens next. Updated at the close of every plan week;
if this file is stale, the week is not closed.

---

## 1. Vision & positioning

**Deterministic first responder for service-mesh incidents: detects in
seconds, opens an evidence-dossier PR, zero LLM, zero cluster mutation.**

MeshMedic watches mesh telemetry (Prometheus), matches a human-reviewed
catalog of failure scenarios, and answers with a GitOps pull request that
carries the diagnosis, labeled evidence, configuration reads, and a
rollback plan. When the failure is outside the catalog, a deterministic
triage layer assembles a dossier instead: who stopped calling, which
workloads are logging known failure signatures, and what rolled out
recently with the exact template diff. The philosophy is the antivirus
split: signatures are authored and reviewed offline; the scanner runs
deterministically online.

**Non-goals (fixed):** no ask/Q&A interface, no generic-Kubernetes breadth
(that lane belongs to Robusta Classic and k8sgpt), no APM/trace
integrations. **This product is pure-deterministic by commitment; that is
the identity, not a limitation to grow out of.** An LLM-powered MeshMedic is
a separate future version, never this one: the moment an LLM drives detection
or writes an applied patch, the differentiators (zero cost, determinism,
reproducibility, safety) evaporate and it becomes a k8sgpt/Holmes me-too.
Bolting an LLM onto deterministic detection can make it worse, not better: it
wraps harmless findings in confident wrong fixes. No optional narration layer
here either until the deterministic core is done.

**Coverage philosophy (honest):** total scenario coverage is impossible for
any tool, deterministic or LLM (Holmes tops out at 86% on its own evals), so
100% is explicitly not the goal. The goal is: cover the common failure
*classes* (the power-law head, a few dozen class-level signatures, each
matching many instances); degrade gracefully on the tail (generic triage
returns a partial dossier, never nothing); and grow coverage through the F9
learn-record-curate loop. An asymptote toward comprehensive, never complete,
honest about the edge. Stating what the tool cannot see is a credibility
feature, not a weakness.

## 2. Measured state (as of 2026-07-18)

Engine v0.3 - all verified by unit tests (7/7 packages) and live bench runs:
- Labeled evidence (`prom.QuerySeries`): breakdowns keep workload names.
- Configuration evidence (`pkg/kube`, kubectl-only, read-only): object
  fields, list mode for policies.
- Cascade suppression (`suppresses:`): one incident, not two, in overflow
  storms.
- Ambient L4 detection: `istio_tcp_connections_closed_total`
  `response_flags="DENY"` from ztunnel - strict-mTLS conflicts that never
  reach L7 metrics.
- Deterministic triage layer: absence signal (`or vector(0)` + `offset`),
  namespace log-signature sweep, ReplicaSet rollout diff; `report-only`
  scenarios produce a dossier instead of a patch.
- Catalog: 19 entries, every signal validated by injecting the fault on the
  testbed and observing real telemetry before merge. `no-route-blackhole` (NR,
  404 no-route) is the first source-keyed entry: live validation found that a
  no-route request carries `destination_service_name=unknown`, so it keys on
  `source_workload_namespace` and names the black-holed caller, with the
  namespace VirtualServices listed so the broken route is visible. Taxonomy
  waves added
  `authz-deny-flood` (403), `route-timeout-too-short` (504/UT), and
  `fault-injection-left-in-production` (FI) - all report-only, the last two
  suppressing error-surge; and enriched `upstream-host-ejection-flood` with
  DestinationRule object evidence to disambiguate UH causes. The triage layer
  generalized to three wrong-target client signatures (`client-dns-typo`
  NXDOMAIN, `client-wrong-port` empty-reply, `client-wrong-scheme` TLS error)
  through one report-only entry. The testbed grew a dependency chain (payments
  now calls a downstream `ledger` service via fake-service `UPSTREAM_URIS`,
  healthy by default), which unlocks the dependency-layer incident family;
  `upstream-dependency-errors` and `upstream-dependency-latency` are the first
  two, both source-keyed on the watched service's outbound telemetry. Errors: a
  downstream at ERROR_RATE 0.8 surfaced as payments' outbound 5xx, the report
  named `ledger`, and it suppressed `error-surge` so the healthy front service
  is not ejected for a fault one hop downstream. Latency: a downstream at 300 ms
  drove payments' outbound p99 to 497 ms, the report named `ledger`, and it
  suppressed `latency-regression-vs-baseline` so the blocked service is not
  blamed for its dependency's slowness. Both suppressions proven live. Bench: 11
  scenarios.
  The testbed then grew a north-south ingress Gateway (istio class) fronting
  payments, unlocking the edge-incident family; `ingress-edge-outage` is the
  first: an HTTPRoute backend pointed at a dead port surfaced as the gateway
  returning 500 to users, and the dossier listed the HTTPRoutes with the broken
  `port: 9999` visible.
  The testbed also grew two EnvoyFilter faults on the payments waypoint (applied
  only as faults, not part of the healthy testbed): a local rate limit, caught
  by `rate-limit-throttling` on the RL flag (429); and an ext_authz filter
  calling a deny-all authz service, caught by `external-authz-denial` on the
  UAEX flag (403), distinct from the native-policy DENY of `authz-deny-flood`.
  Both name the affected caller and list the namespace EnvoyFilters.
  Catalog target is roughly 24-28 validated common classes (the power-law head),
  not a vanity count; the dependency chain, ingress gateway, sidecar mode, rate
  limit, and external authz are done. The distinct-class ceiling is honest: some
  mesh failures are the same class at a different hop (a 404 is a 404), so an
  entry is added only when the class is genuinely distinct, not to reach a
  number. Remaining candidates: ingress TLS/cert (likely an object-state check,
  not telemetry) and the connection-failure family (UF/UC/UR) once an injectable
  mechanism is found.
- Taxonomy tiers 1-3 complete: 36 candidates processed, each validated on the
  testbed or deferred with a documented finding (kube-state-metrics gap,
  no downstream/ingress/sidecar/multi-cluster on the testbed, or subsumed by
  an existing entry). Findings in bench `docs/taxonomy/validation-queue.md`.
- Mode-agnostic detection (validated): the entries querying
  `reporter=~"destination|waypoint"` fire in classic sidecar mode as well as
  ambient. A sidecar-injected `orders` service in `demo-sidecar`
  (`demo/manifests/sidecar-orders.yaml`) reports with `reporter=destination`;
  `error-surge` fired on it at a 1.0 ratio over the 120s for-duration
  (`demo/sidecar-mode/`). This validates the claim rather than asserting it. It
  adds no catalog entry (the same class in another mode would be padding).
  Inherently-ambient entries (`waypoint-overload-scale`, `mtls-policy-conflict-ambient`)
  stay ambient; the `reporter="waypoint"`-only entries need their own sidecar
  validation before the reporter is widened.
- Baseline memory (`pkg/baseline`): EWMA per signal with atomic persistence
  and relative thresholds (`baselineMultiplier`), so a scenario can fire on a
  deviation from a target's own learned normal instead of a fixed number.
  Warm-up guardrail (static threshold until enough healthy samples); only
  non-breaching values feed the baseline. Unit-tested and live-validated: the
  `latency-regression-vs-baseline` entry (the first to use a relative
  threshold) fired when `payments` p99 hit 488 ms against a 140.8 ms threshold
  (3x the learned 47 ms normal), a regression a static 1000 ms threshold would
  miss; labeled evidence named the regressed subset (`v2` 493 ms, `v1` 47 ms).
  Run captured in `demo/baseline-relative/`. A bench scenario for this class
  needs the harness to warm up a baseline before injecting, which the current
  inject-then-measure harness does not do (a documented defer).
- Closed-loop resolution: when a firing incident's signal falls back under its
  threshold, the detector emits a resolution report with the interval the
  condition held (MTTR), completing the lifecycle detect -> dossier/PR ->
  resolved. Only the firing-to-clear edge resolves; a breach that never fired,
  or one whose traffic vanishes, produces no false recovery. Unit-tested and
  live-validated: an incident opened at 05:28:07Z and closed at 05:31:22Z with
  `resolved after 3m15s` (`demo/closed-loop/`).
- Unmatched-incident recorder (`pkg/recorder`, F9): baselines a set of generic
  anomaly signals per target and appends a fingerprint when one deviates while
  no catalog scenario is active. Records only, human-curated; the guardrail
  against learning noise into confident wrongness. Unit-tested and live-
  validated on the testbed: a 250 ms latency regression drove `payments` p99 to
  478 ms (learned normal 48 ms), too small to trip the 1000 ms `canary-latency`
  threshold, so the catalog stayed silent while the recorder logged 13
  fingerprints and the baseline held frozen at 48 ms (the anomaly never became
  the new normal). Run captured in `demo/f9-recorder/`.

Benchmark ([mesh-incidents-bench](https://github.com/kassvl/mesh-incidents-bench)):
11 reproducible mesh-incident scenarios with a scoring rubric. MeshMedic's own
scores are a disclosed home game (`results/meshmedic.md`); the earlier
cross-tool leaderboard against HolmesGPT and k8sgpt was removed as a category
error (those are general Kubernetes tools with zero Istio scenarios in their own
corpora - see honesty rails). The fair, same-domain comparison is now
`istioctl analyze`, Istio's own config analyzer: measured live, it catches
invalid config (IST0101 subset reference) but is blind to runtime incidents (a
live 5xx surge reads as "No validation issues found"). Complementary, not a
contest - config linter vs runtime detector (`results/istioctl-analyze.md`).
Harness measures per run: tool wall time and cluster objects
created/deleted during investigation (MeshMedic: always 0).

## 3. Competitive picture

MeshMedic is a runtime mesh-incident detector, a category with no direct
competitor; the honest comparisons are to adjacent tools on the slice where
they overlap.

- **`istioctl analyze`** (Istio's own): the fair, same-domain reference. A
  static config linter - it validates Istio configuration before traffic and
  catches invalid references and mode conflicts, which MeshMedic does not do,
  but it is blind to runtime incidents (a live 5xx surge reads clean).
  Complementary: lint config in CI, detect incidents with MeshMedic. Measured in
  bench `results/istioctl-analyze.md`.
- **Kiali**: the Istio observability console. Shows mesh health and config
  validation for a human to read; MeshMedic automates the first response Kiali
  makes you read, and outputs a PR rather than a dashboard.
- **Robusta Classic**: the closest prior art for the *approach* - deterministic
  playbooks, log enrichment, change tracking, generic K8s, Slack-bound output.
  Robusta proved deterministic enrichment works; MeshMedic makes it mesh-native
  and PR-native.
- **General Kubernetes / LLM-SRE tools (k8sgpt, HolmesGPT)**: different domain,
  not rivals. They troubleshoot pods, logs, and traces; neither targets the mesh
  (HolmesGPT's own 266-fixture corpus has zero Istio scenarios). Removed from
  the benchmark as a category error; see honesty rails.
- **Commercial AIOps** (Datadog Watchdog, Dynatrace Davis): change correlation
  behind closed doors; not comparable or citable in detail.

Structural properties that define the wedge: continuous watch (seconds-level
MTTD), zero marginal cost per target, zero data egress, reproducible evidence
(every claim is a re-runnable query), and zero cluster mutation by design.

## 4. Firsts registry

Every claim below was preceded by a prior-art search (dated); absence of
evidence is not proof - claims are staked by public commits and revised
if counter-examples surface.

| # | claim | status | evidence | caveats |
| --- | --- | --- | --- | --- |
| F1 | First OSS tool detecting ambient strict-mTLS denials from ztunnel L4 telemetry | **Staked** | meshmedic `f1fb440` (catalog/mtls-policy-conflict-ambient.yaml); bench raw `mtls-conflict-meshmedic-20260717-222548.txt` | Prior-art search 2026-07-18: no GitHub equivalent found |
| F2 | First reproducible mesh-incident diagnosis benchmark (scenarios + rubric) | **Staked** | bench `aa818f1`; 11 scenarios with ground truth and a rubric | Chaos tools inject but do not score diagnosis; academic LLM evals are not mesh benchmarks. Single-author home game; `istioctl analyze` is the fair same-domain reference, independent mesh scenarios are the standing fix |
| F3 | First published "investigation footprint" metric (cluster mutations by a diagnostic tool while it investigates) | **Staked** | bench `docs/investigation-footprint.md`; harness footer (commit `dd68e82`) | The metric and harness method are the first; MeshMedic's zero is a structural read-only guarantee. Tool-agnostic; no cross-tool table (that would need other mesh runtime tools, of which there are none) |
| F4 | First practical reference for ztunnel L4 denial telemetry | **Staked** | bench `docs/ambient-l4-denial-telemetry.md`; labels verified live on Istio 1.24.1 | Doc pins version; signal shape is inherent to ambient mTLS |
| F5 | First comprehensive ambient-mesh failure-mode encyclopedia | Planned (W2) | taxonomy pipeline | - |
| F6 | ~~First MTTD comparison across mesh troubleshooting tools~~ | Dropped | - | Needs multiple mesh *runtime* tools to compare; there are none (istioctl analyze is a config linter, not a runtime detector, so MTTD is not defined for it). Retired with the cross-tool comparison |
| F7 | ~~First mesh/Istio analyzer for k8sgpt~~ | Dropped | - | Out of scope: building a k8sgpt custom analyzer grows another tool's ecosystem, not MeshMedic. The fair comparison is `istioctl analyze` (same domain); general K8s/LLM tools are a different domain, not a comparison |
| F8 | ~~First controlled same-model comparison: dossier-fed vs agent-driven~~ | Dropped | - | Was framed around HolmesGPT's architecture; retired with the Holmes comparison. The deterministic-vs-LLM thesis lives in the LLM-powered future version, not this product |
| F9 | First mesh tool that learns signatures from the incidents it sees in production, deterministically and human-curated | **Staked** | meshmedic `ad446af` (pkg/recorder + detector anomaly watch); unit-tested + live run in `demo/f9-recorder/` (baseline held frozen while 13 fingerprints logged, catalog silent) | records only; no learned signature can remediate without human review + testbed validation |

## 5. Execution plan (6 weeks, career-first, 10-15 h/wk, $0 budget)

Full plan lives in the session plan file; gates here.

- **W0 - seal the base** (DONE): commits pushed (`f1fb440`, `aa818f1`);
  triage verified on client-dns-typo (dossier shows resolver log line +
  rollout diff `- payments:9090` → `+ payments-svc.demo:9090`); two live
  bugs found and regression-tested (ReplicaSet reuse, fixed-offset
  baseline); error-surge regression clean (no false triage fire);
  scenarios scored and docs updated; holmesgpt scratch clone deleted.
- **W1 - storefront**: README rewrite + demo with dossier scene; bench
  CONTRIBUTING + scenario template; F4 and F3 docs written.
- **W2 - taxonomy wave 1**: 4 sonnet subagents generate grounded
  candidates (Istio issues + public failure catalogs); Fable validates 4-6
  classes on the testbed; catalog 9→13+, bench 6→8; F5 encyclopedia.
- **W3 - baseline memory + unmatched-incident recorder**: `pkg/baseline`
  EWMA store + relative thresholds (the "knows your cluster's normal" moat),
  plus a recorder that logs the telemetry fingerprint of any deviation with
  no matching catalog entry. The recorder is the production-fed, automatic
  version of this session's manual taxonomy wave: the tool accumulates what
  it actually sees. Hard guardrail: it records only. No learned signature
  can drive remediation without human review and testbed validation, the
  same discipline that caught the OOM signal gap, the route-timeout cascade,
  and the wrong-port empty-reply signature live this session. Learn, record,
  human/testbed validates, catalog grows. This is the antivirus/SIEM model,
  not LLM self-training, so it does not compromise determinism.
- **W4 - deepen the system (MeshMedic-native)**: strengthen MeshMedic's own
  capabilities, not another tool's ecosystem. Candidates: closed-loop
  resolution reports (a "resolved" report with MTTR when a signal clears),
  storm scenario (concurrent faults, dedup/suppression under load), richer
  triage (rollout-diff attached to any report-only incident, not just
  traffic-vanished), and more validated catalog classes. The dropped k8sgpt
  analyzer (F7) is explicitly out: it would grow k8sgpt, not MeshMedic.
- **W5 - architecture proof + comparison**: F8 same-model/two-architectures
  comparison with `istioctl analyze` (config linter vs runtime detector),
  recorded. General K8s/LLM tools are a different domain, not rivals, and were
  removed from the benchmark as a category error.
- **W6 - launch (credibility-first)**: the benchmark is the visibility engine,
  not the tool, but only if it survives scrutiny. The "beat Holmes" narrative is
  dead by finding (different domains; Holmes has zero mesh fixtures), so the
  honest gate before any comparative framing is **independent mesh scenarios**
  MeshMedic's author did not write (real Istio incidents from public postmortems
  and issues, contributed by operators). Launch strategy: (1) fix benchmark
  credibility with independent scenarios; (2) lead with the story of the tool
  and the reproducible mesh benchmark, not a leaderboard win; (3) a killer 60s
  demo of detect -> dossier/PR -> resolved; (4) the honest positioning
  "the mesh-telemetry layer general tools do not cover", timely in the AI-SRE
  hype without overclaiming; (5) honesty as a feature (the home game, the
  istioctl comparison, and what the tool cannot do, all stated). Realistic
  ceiling: respected niche noise + a strong career signal, not virality.
  Selling MeshMedic as a "Holmes killer" backfires; the honest wedge earns
  respect.

Execution style: firsts and PRP updates are done by the main model,
one at a time, examine-then-produce; subagents (sonnet) only generate W2
taxonomy candidates. No catalog/scenario entry merges without testbed
validation. Weekly scope = one demoable deliverable. Before every push, run
the same checks CI runs (`go build ./... && go vet ./... && go test ./... &&
validate` for the tool; `shellcheck` for the bench).

Taxonomy validation order: finish the current tier completely before
starting the next. "Finish" means every candidate in the tier is either
validated on the testbed and merged, or deferred with a documented finding.
No skipping ahead to a lower tier or to a later week while the current tier
has unprocessed candidates.

## 6. Honesty rails & limits

- **Home game**: bench and tool share an author; v0.2+ changes were
  developed against these exact scenarios. Stated wherever scores appear;
  outside scenario contributions are the standing fix.
- **No fair head-to-head with Holmes exists** (audited 2026-07-19): Holmes's
  own 266-fixture corpus has zero Istio/mesh scenarios, all plain Kubernetes.
  So "MeshMedic beats Holmes" is not well-formed - they are different-domain
  tools, and the leaderboard measures mesh coverage a general tool does not
  target, not general quality. The positioning is complementary, never "better
  than Holmes." The real credibility fix is independent *mesh* scenarios
  (community-authored), which neither Holmes's corpus nor a home-game bench
  supplies. Attempting to beat Holmes on its own fixtures is a dead end: there
  is no fixture MeshMedic can even play.
- **Ceiling**: deterministic triage covers frequent, signature-bearing
  failure classes (bad deploys, DNS/conn/TLS, config accidents). Novel
  failure modes without signatures, cross-service causal chains, and
  problems that never log are out of reach - by design, disclosed.
- **Signatures rot**: pattern lists need curation; they live in the
  reviewable catalog for that reason.
- **Clock risk**: cheap/local frontier-class models will shrink the
  cost/privacy gap of LLM agents within 1-2 years. The durable moat is
  what we accumulate now: baseline memory, evidence quality, closed loop,
  taxonomy coverage, and the bench community - not LLM-lessness alone.
- **Learning without self-deception**: a tool that "learns from every error"
  is only as good as its guardrail against learning noise. Undisciplined
  self-training is how an LLM layer bolted onto deterministic detection can
  score worse than the deterministic layer alone, wrapping harmless findings in
  confident wrong fixes. The recorder
  (F9) records fingerprints but never auto-promotes them to remediation;
  promotion is a human decision backed by testbed validation. Alertness to
  new failures comes from the deterministic baseline deviation, not from a
  model retraining on unverified input.

## 7. Thesis synergy

`~/istio-ambient-aiops-thesis` shares this project's subject. Designated
shared outputs: the bench methodology, the ambient-mesh failure taxonomy, the
ztunnel L4 telemetry reference, and the mode-agnostic (sidecar vs ambient)
observability finding - each written so a cleaned copy can serve as thesis
chapters. Keep the academic register in
those three docs slightly more formal for reuse.
