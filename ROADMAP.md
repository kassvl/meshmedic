# Roadmap

Milestones are demos, not feature lists. A milestone is done when the thing
can be shown, not when the code merges.

## M1: the 60 second video

One take, no cuts: chaos is injected into a demo mesh, Prometheus fires,
MeshMedic opens a pull request with the patch and the evidence, a human
merges it, the dashboards go green. That video sits at the top of the README
and is the bar for every component below.

Needs, in order:

- [x] Remediation catalog with validated entries and rendered patches
- [x] Detector: evaluate catalog signals against a live Prometheus
- [x] PR opener: render patch + evidence + narrative into a GitHub PR
- [x] Demo environment: kind + Istio (ambient) + demo app + chaos scripts (`demo/`)
- [x] The video (`demo/video/meshmedic-demo.mp4`, every frame from a real episode)

**M1 is done.**

## M2: mesh-incidents-bench

A separate repo of reproducible mesh failure scenarios (grown out of an
ambient-mesh AIOps thesis) with a harness that scores any tool's diagnosis
and remediation against them. Publish results for the obvious contenders.
Nobody has a mesh incident benchmark today; the leaderboard is the point.

## M2.5: win the benchmark outright

The bench (M2) found the gaps; close them and grow the lead where agentic
investigators structurally cannot follow:

- [x] Labeled evidence: per-workload breakdowns survive into the report, so
  the diagnosis names the offending subset, not just a ratio
- [x] Configuration evidence: read-only object reads (kubectl, no client-go)
  put the config-level root cause next to the metric symptom, deterministic
  and LLM-free
- [x] Cascade suppression: overflow 503s inflating the 5xx ratio is one
  incident, not two
- [x] Ambient L4 signal (ztunnel `istio_tcp_connections_closed_total`
  DENY) for the strict-mTLS scenario every tool missed, MeshMedic included
- [x] Bench v2: noise-only scenario (false-positive discipline), wall-time
  and investigation-side-effect metrics per run

**M2.5 is done**: 30/30 on bench v0.2 (a disclosed home game; outside
scenarios welcome), with 0 cluster objects touched across every run.

## M2.6: deterministic triage for out-of-catalog incidents

- [x] Absence signal: `traffic-vanished-triage` fires when traffic that was
  flowing has stopped (`or vector(0)` + a `max_over_time` baseline), the
  failure class no threshold-on-presence detector can see
- [x] Log-signature sweep: read every namespace deployment's recent logs
  for curated failure patterns (resolver/connection/TLS), matching lines
  only, into the report
- [x] Rollout diff: attach the template diff of any deployment that rolled
  out recently (via the Progressing condition, not ReplicaSet age) - a bad
  deploy's root cause is a line in that diff
- [x] `report-only` scenarios: produce an evidence dossier instead of a
  patch when the failing party is not the watched service itself

**M2.6 is done**: bench 36/36 with the client-dns-typo triage scenario;
the mechanism generalizes to any bad client deploy, not just this fixture.

## M2.7: taxonomy expansion and baseline memory

- [x] Taxonomy waves 1-3: 36 candidate failure classes generated and
  processed, each validated on the testbed or deferred with a documented
  finding. New catalog entries: `authz-deny-flood`, `route-timeout-too-short`,
  `fault-injection-left-in-production`; the triage layer generalized to three
  wrong-target client signatures; `upstream-host-ejection-flood` enriched to
  disambiguate UH causes. Catalog 9 to 12, bench 6 to 11 scenarios
- [x] Baseline memory (`pkg/baseline`): EWMA per signal, persisted, with
  relative thresholds (`baselineMultiplier`) so a scenario fires on a
  deviation from a target's own normal. Warm-up guardrail; only healthy
  values feed the baseline. First relative-threshold entry
  `latency-regression-vs-baseline`, live-validated (`demo/baseline-relative/`):
  fired at 3x the learned normal on a regression a static threshold would miss
- [x] Unmatched-incident recorder (F9): `pkg/recorder` appends a fingerprint
  when a baselined anomaly signal deviates while no catalog scenario is
  active. Records only, human-curated; the guardrail against learning noise

**M2.7 is done**: catalog 13, bench 11, plus the baseline-memory and
unmatched-incident-recorder foundations, unit-tested and live-validated on
the testbed (`demo/baseline-relative/`, `demo/f9-recorder/`).

## M2.8: close the loop

- [x] Resolution reports: when a firing incident's signal falls back under its
  threshold, MeshMedic prints a resolution with the interval the condition held
  (MTTR), so the incident opens and closes in the same terminal. Only the
  firing-to-clear edge resolves; a never-fired breach or a no-data clear
  produces no false recovery

**M2.8 is done**: an incident opened and closed on the testbed with
`resolved after 3m15s` (`demo/closed-loop/`), unit-tested on all three edges.

## M3: prove and present the system

- Architecture-proof experiment: the same model fed a MeshMedic dossier in one
  shot versus driving an agent investigation, measured side by side, to show
  the edge is the deterministic pipeline, not the model
- Present MeshMedic to the community (Istio meeting demo, a CFP for a lightning
  talk). This sells our own system; it does not contribute to another tool

## M4: earn the right to be a project

CNCF Sandbox needs adopters and outside contributors. That conversation only
makes sense after M1 through M3 have produced users. Until then, this repo
optimizes for one thing: every scenario it claims to fix, it can demonstrably
fix on camera.
