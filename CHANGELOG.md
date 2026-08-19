# Changelog

All notable changes to MeshMedic. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/kassvl/meshmedic/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/kassvl/meshmedic/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/kassvl/meshmedic/releases/tag/v0.3.0
