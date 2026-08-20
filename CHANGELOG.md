# Changelog

All notable changes to MeshMedic. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **The cycle summary no longer says `firing` for three different things.**
  In Prometheus's vocabulary an alert fires once it has held for its `for`
  duration; this counter turned true the moment the comparison did, which is
  earlier and sometimes much earlier. Two runs were misread because of it: one
  showed `2 firing` with a single report, because the second entry was
  suppressed as a cascade symptom, and one showed `1 firing` with no report at
  all, because the entry had not yet held. The tool was right both times and
  the summary described it wrongly. The line now reads `N in breach` and, when
  N is not zero, breaks it down: reported, suppressed, awaiting hold,
  rate-limited, handler retrying. Every breach lands in exactly one bucket and
  a test asserts the arithmetic, because a number that does not add up is a
  liability rather than a fact. The two silent exits from delivery, a guardrail
  that stopped a further proposal and a handler that errored, are counted
  apart: a guardrail that hides what it blocked is its own fail-open.

- **`connection-pool-overflow` now reports the pool limit that is doing the
  shedding.** The entry read the destination Deployment, which is the right
  evidence for the raise-versus-scale call, and never opened the
  DestinationRule that held the fault, so a dossier that correctly diagnosed
  pool exhaustion still ended with the operator looking up the number to
  change. Measured on 2026-08-19 with `maxConnections: 1` as the injected
  fault and no mention of it in the report. Every DestinationRule in the
  namespace is listed rather than one guessed by name, because a rule that
  binds a host is not obliged to be named after it. An audit of the other
  eleven entries carrying object evidence found no second case: each already
  reads the object that holds its fault.

### Added

- **A mutation suite over the guards themselves.** Every guard here claims to
  catch something and nothing checked the claim, which stopped being an
  abstract worry on 2026-08-20: four separate checks turned out to be checking
  nothing. A preflight called a testbed quiet on the strength of a dead
  Prometheus. A summary printed FAIL for a run nobody observed. A coverage
  count measured a command-line filter. An archive assertion in CI failed on
  its own pipe before it read an archive. All four were green.

  So the shipped catalog and specs are now broken on purpose, one property at
  a time, and the guard that owns that property has to reject the result. A
  mutation that survives is a guard that is decorative. These are the ones
  catchable without a cluster: validation, the deadline-versus-hold rule, the
  cross-directory entry check, and the lock.

  Two of them pin down exactly where the lock's line sits, which had never
  been written down as a test. Comments and blank lines are free, verified by
  reformatting every catalog file on disk and reloading it, because a lock
  that forces re-approval for reindentation teaches people to re-approve
  without reading. Rewriting the Diagnosis paragraph is not free, because that
  paragraph is not prose about the entry, it is what the incident report tells
  an operator mid-incident.

  Writing this suite produced two false alarms of its own, both left in the
  record. The first reached for `Stale`, which answers a different question,
  and reported sixteen surviving mutations that were artefacts of the wrong
  call. The second expected the Diagnosis text to be free and was simply
  wrong about what that field is. A red check is no more proof that its
  subject is broken than a green one is proof that it works, which is the
  entire thesis of the suite.
- **A proof whose deadline sits inside its entry's hold duration is now
  refused before anything is injected.** `Spec`'s documentation has always
  said `firesWithin` must exceed the hold or the proof is unwinnable, and
  nothing enforced it: `Validate` checks the deadline is positive and stops,
  because a spec on its own cannot see the catalog. So the rule lived in a
  comment and holding to it was a matter of remembering.

  It nearly cost a run the same day. An entry's hold went from 90 seconds to
  five minutes and its proof's five-minute deadline had to be raised by hand
  in the same edit. Forgetting would have produced "never fired within 5m",
  which reads as a broken entry and is in fact a deadline expiring at the
  exact moment the entry became eligible to report: a measurement about
  arithmetic wearing the costume of a measurement about the mesh.

  Nothing in the repository violates this today, and a test over the shipped
  catalog and proof directories is what keeps that true. A proof that can be
  won but leaves under a minute of slack is said rather than refused, since
  the signal still has to climb into breach and the detector still needs a
  tick to deliver, both inside that.
- **`meshmedic-prove silence`**, the axis the proof suite could not reach.
  Every spec in `proof/` asserts that an entry fires when its fault is present.
  None of them assert that it stays quiet when it is not, and a detector that
  fires on everything passes all of them. This injects nothing and watches a
  healthy cluster with the real detector, so hold durations, suppression, the
  coverage probe and the guardrails are all in force.

  It is a different question from `meshmedic calibrate`, which samples the
  signal and reports how far the healthy extreme sits from the threshold.
  Calibrate asks how close the number is; this asks whether anyone would have
  been paged. The gap between those was measured on 2026-08-19: an entry
  entered breach on a cluster with nothing injected into its path and stayed
  there for 180 unbroken seconds, twice its hold duration, and the only reason
  it published nothing is that the observation window closed 25 seconds in.
  Nothing in the suite was looking for it.

  Five verdicts, and the distinctions are the point. `silent` never crossed;
  `brushed` crossed but not for long enough, and reports the margin between the
  longest healthy breach and the hold duration, which is what an ordinary busy
  hour eats into; `FIRED` is a false positive, measured; `unwatched` produced
  no readings, so its silence proves nothing; `n/a` was never a question for
  that target. A thin margin warns without failing the run, because it is a
  warning about tomorrow rather than a fault today. `--warmup` learns the
  baseline first, so relative thresholds are measured as production uses them
  rather than against a static fallback production never sees.
- **`--entry` is repeatable.** Proving two entries used to mean invoking the
  command twice, which skips the quiesce between them, because the quiesce
  lives between specs inside one run. That is not hypothetical: the second of
  two such runs was refused by its own preflight, correctly, because the entry
  it was about to test was still breaching from the first run's reset. One
  invocation with two `--entry` flags settles between them. Every id that
  matched nothing is named, not just the first, because a typo in one of five
  entries should not read as a run of four.
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
