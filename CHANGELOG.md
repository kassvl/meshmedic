# Changelog

All notable changes to MeshMedic. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
