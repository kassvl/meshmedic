# Changelog

All notable changes to MeshMedic. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`meshmedic-prove doctor`.** The seven preflight checks as their own
  command, injecting nothing, with an exit code a script can branch on. They
  existed already but only as the opening act of a run that then mutates the
  cluster, so the question "is this testbed fit to measure anything" could not
  be asked without answering it destructively. It was asked by hand five times
  in one session before a suite was trusted to start.
- **`--wait-observer` on the prover.** Before each proof, wait for Prometheus
  to answer rather than injecting into a cluster nobody is watching. A suite
  outlives the port-forward that was up when it started, and the previous
  behaviour turned one transient gap into a row of verdicts about entries that
  were never observed. Nothing is injected while the wait is on, so the
  alternative to waiting was never a stricter measurement.
- **`--retry-blind` on the prover**, and `proof.Retryable` behind it. Only a
  blind run is retried. A run where the observer was healthy and the entry did
  not fire is a finding, and a harness that retries findings until they
  disappear is a machine for producing green. This is the same line the
  detector draws between `blind` and `clear`.
- **`--results` on the prover**, writing the run as JSON. Eleven scenarios were
  scored by hand into a scratch file on 2026-08-19 for want of this.
- **`hack/port-forward.sh`**, a forward that survives a long suite. It waits for
  the listening socket to drain before rebinding, because restarting
  immediately loses the race against its own predecessor and turns a
  one-second gap into thirty, which is long enough for a detector to report
  blind and a harness to blame an entry.

### Fixed

- **The prover's preflight could call a testbed quiet on the strength of a dead
  Prometheus.** Its breach check treated any query error as "no series", which
  is the healthy reading for a failure counter but is not what a refused
  connection means. Empty results and unreachable servers are now distinct, and
  entries that could not be read fail the check by count. This is precisely the
  blind-versus-clear confusion the detector's four-state model exists to
  prevent, committed by the harness that checks for it.

## [1.0.0] - 2026-08-19

### Added

- **Four-state evaluation: `firing`, `clear`, `blind`, `unlocked`.** An empty
  query result, a zero value, and a query error are three different facts and
  now stay distinct all the way to the report. Previously all three read as
  silence, so the tool could not tell you whether it was quiet because nothing
  was wrong or quiet because it was blind.
- **Coverage probe per target, every cycle.** A target whose control query
  returns no series is *unobserved*, and every scenario against it reports
  `blind` rather than `clear`. `watch` prints the coverage line each cycle.
  Entries may declare `absenceIsSignal: true` so that a failure whose whole
  symptom is stopped telemetry still works; the probe, not the scenario query,
  is what separates "the traffic stopped" from "we cannot see this target".
- **`meshmedic check`** evaluates every target once and exits non-zero if any
  is unobserved, so a blind detector fails a readiness check instead of
  looking healthy.
- **Persisted incident state** (`stateFile`). A restart no longer re-opens
  incidents that are still open. The state key is derived from the target's
  parameters rather than its position in the config, so reordering the target
  list does not reassign open incidents either.
- **`validate --against-prometheus URL`.** Label sets were verified live on
  one Istio version; when Istio renames a label the affected PromQL silently
  never fires again, because a matcher on a missing label matches nothing
  rather than erroring. Coverage is lost with no signal at all, and is
  discovered during an incident. This extracts the metric names and label keys
  every entry references and confirms each exists on a live server, reporting
  `ok` / `metric missing` / `label missing` per scenario, with `--strict` for
  CI. Extraction tokenizes PromQL rather than pattern-matching it, so a brace
  inside a string literal is never mistaken for a selector, and does so without
  taking a dependency on `github.com/prometheus/prometheus`, which would bring
  253 modules into a build that has one.
  First result on the stock Istio addon's telemetry: seventeen of nineteen
  entries resolve. `retry-storm-damping` needs `envoy_cluster_upstream_rq_retry`
  and `waypoint-overload-scale` needs `kube_pod_status_ready`, neither of which
  the addon scrapes.
- **`catalog.lock` and `meshmedic approve`.** The catalog directory was both
  the editable surface and the enforced artifact, so nothing established that
  the entry running was the entry that was reviewed and testbed-validated. The
  lock records a content hash per entry plus what it was validated against
  (Istio version, testbed commit). An entry whose hash is missing or stale is
  `unlocked`: it does not run, and it is reported every cycle rather than
  dropped. `watch --strict` and `validate --strict` refuse outright;
  `validate --no-drift` is the CI gate and fails only when an *approved* entry
  was edited without re-approval. `approve` is the only writer, is human-only,
  and refuses an entry with no recorded validation.
  The hash covers the parsed entry rather than the file bytes, so reformatting,
  reordering keys, or editing a comment does not invalidate an approval, while
  any change to a threshold, query, patch template, or guardrail does.
- **`maxAppliesPerHour` is enforced.** All nineteen entries declared an hourly
  limit and no code read it; the safety story promised a rate limit that did
  not exist. The limit now applies per scenario and target, its window
  persists with the rest of the state so restarting the process cannot buy
  extra applies, and hitting it logs loudly rather than silently dropping the
  incident.
- **Config-free watch.** `meshmedic watch --prometheus URL --target k=v,k=v`
  runs against a Prometheus you already have, with no YAML file and no demo
  testbed. Opening pull requests still requires a config file, deliberately:
  write access to a repository is not something to enable from a flag pasted
  out of a README.
- **Container image** on GHCR, multi-arch (linux/amd64, linux/arm64), built on
  distroless and running as `nonroot`. The catalog is baked in at
  `/etc/meshmedic/catalog` and pointed to by `MESHMEDIC_CATALOG`.
- **Prebuilt binaries** for linux and darwin on amd64 and arm64, released by
  GoReleaser. Each archive carries the catalog, because a binary without it is
  not runnable.
- `MESHMEDIC_CATALOG` environment variable as the default for `--catalog`.
- `meshmedic version`.

### Changed

- `watch --config` no longer defaults to `watch.yaml` when `--prometheus` is
  given; the two forms are mutually exclusive and say so.

## [0.3.1] - 2026-07-19

### Added

- `external-authz-denial`: traffic denied by an external authorization service
  (403/UAEX), distinct from the native-policy DENY of `authz-deny-flood`.
- `rate-limit-throttling`: traffic rejected by a local rate limit (429/RL).
- `ingress-edge-outage`: users getting 5xx at the north-south ingress gateway.
- `upstream-dependency-errors` and `upstream-dependency-latency`: a service
  failing or slow because something it calls is. Both suppress the entry that
  would otherwise blame the healthy front service.
- `no-route-blackhole`: requests matching no route (404/NR). Source-keyed,
  because live validation showed a no-route request carries
  `destination_service_name=unknown`.
- Sidecar-mode testbed, proving the mode-agnostic entries fire in classic
  sidecar mode as well as ambient.

### Changed

- A signal template needing a parameter the target does not define is treated
  as not-applicable and logged once, instead of erroring every tick.

## [0.3.0] - 2026-07-19

### Added

- **Baseline memory** (`pkg/baseline`): EWMA per signal, persisted atomically,
  with relative thresholds so a scenario fires on a deviation from a target's
  own learned normal. Warm-up guardrail; only healthy values feed the baseline.
- **Closed-loop resolution reports**: a firing incident whose signal falls back
  under its threshold emits a resolution carrying the interval it was open.
- **Unmatched-incident recorder** (`pkg/recorder`): a fingerprint is appended
  when a baselined signal deviates while no catalog entry explains it. Records
  only; no learned signature drives remediation without human review.
- **Deterministic triage layer**: absence signal, namespace log-signature
  sweep, and ReplicaSet rollout diff, delivered as a dossier for `report-only`
  scenarios.

### Fixed

- Rollout recency now reads the Deployment's `Progressing` condition instead of
  ReplicaSet age: Kubernetes reuses an existing ReplicaSet when a rollback
  restores an old template, so creation timestamps lie about when a rollout
  happened.
- The traffic-vanished baseline uses `max_over_time` rather than a fixed
  offset, which went blind inside back-to-back outages.

[Unreleased]: https://github.com/kassvl/meshmedic/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/kassvl/meshmedic/compare/v0.3.1...v1.0.0
[0.3.1]: https://github.com/kassvl/meshmedic/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/kassvl/meshmedic/releases/tag/v0.3.0
