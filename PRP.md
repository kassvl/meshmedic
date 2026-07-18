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
integrations. An optional LLM layer may *narrate* a finished dossier; it
never drives detection and MeshMedic must remain complete without it.

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
- Catalog: 9 entries, every signal validated by injecting the fault on the
  testbed and observing real telemetry before merge.

Benchmark ([mesh-incidents-bench](https://github.com/kassvl/mesh-incidents-bench)) v0.2 leaderboard (6 scenarios, 36 pts):

| tool | fault (4) | noise-only | client-dns-typo | total |
| --- | --- | --- | --- | --- |
| MeshMedic (home game, disclosed) | 24/24 | 6/6 | 6/6 (v0.3 triage) | 36/36 |
| HolmesGPT (mistral-large) | 11/24 | 3/6 | 0/6 | 14/36 |
| k8sgpt (no AI / AI) | 0/24 | 4/6 / 2/6 | 0/6 | 4 / 2 of 36 |

Harness measures per run: tool wall time and cluster objects
created/deleted during investigation (MeshMedic: always 0).

## 3. Competitive picture

- **HolmesGPT** (CNCF Sandbox, LLM-agentic): excellent open-ended reasoning
  with strong models; measured weaknesses on our bench (mesh blindness,
  step-budget exhaustion, provider fragility) and in its own published
  evals (chain-of-causation 0-71%, context_window down to 14%). Full
  analysis: bench `docs/holmes-weakness-map.md`.
- **k8sgpt** (CNCF Sandbox, deterministic analyzers): object-state only;
  structurally blind to telemetry-layer incidents (measured 0/24). AI mode
  scored *below* its own no-AI mode on false-positive discipline.
- **Robusta Classic**: the closest prior art - deterministic playbooks,
  log enrichment, change tracking, generic K8s, Slack-bound output. Our
  honest one-liner: *Robusta Classic proved deterministic enrichment
  works; MeshMedic makes it mesh-native and PR-native.*
- **Commercial AIOps** (Datadog Watchdog, Dynatrace Davis): change
  correlation behind closed doors; not comparable or citable in detail.

Structural advantages no LLM agent can copy without ceasing to be one:
continuous watch (seconds-level MTTD), zero marginal cost per target,
zero data egress, reproducible evidence (every claim is a re-runnable
query), storm behavior, and closed-loop potential.

## 4. Firsts registry

Every claim below was preceded by a prior-art search (dated); absence of
evidence is not proof - claims are staked by public commits and revised
if counter-examples surface.

| # | claim | status | evidence | caveats |
| --- | --- | --- | --- | --- |
| F1 | First OSS tool detecting ambient strict-mTLS denials from ztunnel L4 telemetry | **Staked** | meshmedic `f1fb440` (catalog/mtls-policy-conflict-ambient.yaml); bench raw `mtls-conflict-meshmedic-20260717-222548.txt` | Prior-art search 2026-07-18: no GitHub equivalent found |
| F2 | First reproducible mesh-incident diagnosis benchmark with a tool leaderboard | **Staked** | bench `aa818f1`; README leaderboard | Chaos tools inject but do not score diagnosis; academic LLM evals are not tool leaderboards |
| F3 | First published "investigation footprint" measurement (cluster mutations by diagnostic tools) | **Staked** | bench `docs/investigation-footprint.md`; harness footer (commit `dd68e82`); Holmes v0.1 canary created 5 pods, cited from tool-call log | v0.2 runs measured 0 for all tools; the metric+method is the first, MeshMedic's zero is structural, agents' is per-run |
| F4 | First practical reference for ztunnel L4 denial telemetry | **Staked** | bench `docs/ambient-l4-denial-telemetry.md`; labels verified live on Istio 1.24.1 | Doc pins version; signal shape is inherent to ambient mTLS |
| F5 | First comprehensive ambient-mesh failure-mode encyclopedia | Planned (W2) | taxonomy pipeline | - |
| F6 | First MTTD comparison across mesh troubleshooting tools | Planned (W2) | harness timestamps exist | - |
| F7 | First mesh/Istio analyzer for k8sgpt (inside a CNCF project) | Planned (W4) | - | verify none merged upstream before PR |
| F8 | First controlled same-model comparison: dossier-fed single-shot vs agent-driven investigation | Planned (W5) | free via mistral tier | doubles as thesis material |
| F9 | First mesh tool that learns signatures from the incidents it sees in production, deterministically and human-curated | Planned (W3) | unmatched-incident recorder, adjacent to baseline memory | records only; no learned signature can remediate without human review + testbed validation |

## 5. Execution plan (6 weeks, career-first, 10-15 h/wk, $0 budget)

Full plan lives in the session plan file; gates here.

- **W0 - seal the base** (DONE): commits pushed (`f1fb440`, `aa818f1`);
  triage verified on client-dns-typo (dossier shows resolver log line +
  rollout diff `- payments:9090` → `+ payments-svc.demo:9090`); two live
  bugs found and regression-tested (ReplicaSet reuse, fixed-offset
  baseline); error-surge regression clean (no false triage fire);
  leaderboard 36/36 and docs updated; holmesgpt scratch clone deleted.
- **W1 - storefront**: README rewrite + demo with dossier scene; bench
  CONTRIBUTING + scenario template; F4 and F3 docs written.
- **W2 - taxonomy wave 1**: 4 sonnet subagents generate grounded
  candidates (Istio issues + Holmes corpus); Fable validates 4-6 classes
  on the testbed; catalog 9→13+, bench 6→8; F5 encyclopedia + F6 MTTD.
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
- **W4 - ecosystem**: k8sgpt analyzer PR (F7, intent-issue first) +
  Istio community meeting demo request.
- **W5 - closed loop + storm + architecture proof**: resolution reports;
  storm scenario; F8 experiment recorded.
- **W6 - launch**: gate = ecosystem PR live + docs polished + leaderboard
  current + demo video; then Show HN / r/kubernetes / Istio Slack, CFP
  draft. CNCF Sandbox talk only after real adopters (M4).

Execution style: firsts and PRP updates are done by the main model,
one at a time, examine-then-produce; subagents (sonnet) only generate W2
taxonomy candidates. No catalog/scenario entry merges without testbed
validation. Weekly scope = one demoable deliverable.

## 6. Honesty rails & limits

- **Home game**: bench and tool share an author; v0.2+ changes were
  developed against these exact scenarios. Stated wherever scores appear;
  outside scenario contributions are the standing fix.
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
  self-training is how k8sgpt's AI mode scored below its no-AI mode on
  noise-only, wrapping harmless findings in confident fixes. The recorder
  (F9) records fingerprints but never auto-promotes them to remediation;
  promotion is a human decision backed by testbed validation. Alertness to
  new failures comes from the deterministic baseline deviation, not from a
  model retraining on unverified input.

## 7. Thesis synergy

`~/istio-ambient-aiops-thesis` shares this project's subject. Designated
shared outputs: the bench methodology, the Holmes weakness analysis, and
the W5 same-model/two-architectures experiment (F8) - each written so a
cleaned copy can serve as thesis chapters. Keep the academic register in
those three docs slightly more formal for reuse.
