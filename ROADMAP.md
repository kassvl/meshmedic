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
- [ ] Demo environment: kind + Istio (ambient) + demo app + chaos scripts (`demo/`)
- [ ] The video

## M2: mesh-incidents-bench

A separate repo of reproducible mesh failure scenarios (grown out of an
ambient-mesh AIOps thesis) with a harness that scores any tool's diagnosis
and remediation against them. Publish results for the obvious contenders.
Nobody has a mesh incident benchmark today; the leaderboard is the point.

## M3: live inside the ecosystem

- k8sgpt custom analyzer covering the catalog's detection side
- HolmesGPT toolset contribution so its agent can read mesh state properly
- Demo at an Istio community meeting, then a CFP for a lightning talk

## M4: earn the right to be a project

CNCF Sandbox needs adopters and outside contributors. That conversation only
makes sense after M1 through M3 have produced users. Until then, this repo
optimizes for one thing: every scenario it claims to fix, it can demonstrably
fix on camera.
