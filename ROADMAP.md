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

## M3: live inside the ecosystem

- k8sgpt custom analyzer covering the catalog's detection side
- Demo at an Istio community meeting, then a CFP for a lightning talk

## M4: earn the right to be a project

CNCF Sandbox needs adopters and outside contributors. That conversation only
makes sense after M1 through M3 have produced users. Until then, this repo
optimizes for one thing: every scenario it claims to fix, it can demonstrably
fix on camera.
