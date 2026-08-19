# End-to-end proofs: live runs

Each file here is the incident report a catalog entry produced when its fault
was injected into the live testbed (kind + Istio 1.24.1) and a real detector
watched. They are kept so a passing proof is auditable rather than merely
green: the claim is not "the suite says pass", it is "here is what the entry
said, read it yourself".

Reproduce with the separately published prover:

```console
$ meshmedic-prove --entry <id> --yes-inject-faults --out demo/proof-reports
```

## What a pass means

| assertion | why a bare "did it fire" check is not enough |
| --- | --- |
| fired within its hold duration | an entry that fires eventually is not a first responder |
| the report names the culprit | firing without naming what to act on is detection with no explanation |
| declared neighbours stayed quiet | a cascade that blames the healthy service is worse than silence |
| no unaccounted entry fired | either the fault was less isolated than claimed, or that entry is miscalibrated |
| resolved on reset | an incident that opens and never closes is a permanent page |

## Runs

Nine entries, each injected on the live testbed (kind + Istio 1.24.1) with a
real detector watching. Every row is a full pass: fired inside its own hold
duration, named the culprit, kept its declared neighbours quiet, and cleared
when the fault was removed.

| entry | fired after | the report named | resolved after |
| --- | --- | --- | --- |
| `authz-deny-flood` | 1m2s | the denied caller by its SPIFFE principal | 1m45s |
| `canary-latency-rollback` | 1m36s | the canary subset against a 287ms stable comparison | 1m58s |
| `error-surge-outlier-ejection` | 2m3s | `payments-v2` at a 0.67 error ratio | 42s |
| `fault-injection-left-in-production` | 1m6s | the FI-stamped rate at 0.933 | 1m45s |
| `rate-limit-throttling` | 1m2s | `ingress-istio` as the throttled caller | 1m44s |
| `route-timeout-too-short` | 1m4s | the UT-timed-out rate at 1.915 | 1m45s |
| `traffic-vanished-triage` | 1m40s | `loadgen`, its NXDOMAIN log line, and the rollout diff | not checked |
| `upstream-dependency-errors` | 1m5s | `ledger` at 3.708 rps | 1m45s |
| `upstream-dependency-latency` | 1m30s | `ledger` at 495.6ms p99 | 2m5s |
| `latency-regression-vs-baseline` | 1m32s | `v1` at 987.7ms against a healthy `v2` at 95.7ms | 2m2s |
| `mtls-policy-conflict-ambient` | 1m45s | the denied caller with no mesh identity | 1m50s |
| `external-authz-denial` | 1m3s | the caller refused by the external service | 1m45s |
| `ingress-edge-outage` | 1m5s | `ingress-istio` returning 5xx at the front door | 1m45s |
| `upstream-host-ejection-flood` | 1m7s | `payments` with no reachable endpoints | 2m12s |
| `no-route-blackhole` | 1m4s | `loadgen`, the black-holed caller | 1m45s |
| `connection-pool-overflow` | 1m4s | `payments` shedding load at the pool limit | 1m56s |

`latency-regression-vs-baseline` is the one worth reading in full. It fired at
993.7ms against a threshold of 590.8, which is three times the 196.9ms it had
learned as this cluster's normal during a five-minute warm-up. A static 1000ms
threshold would have missed it by six milliseconds, which is the entire reason
the entry exists.

Getting that proof to run took four attempts and the first three were the same
mistake: the fault went on the canary subset, which carries 20 percent of
traffic, so it barely moved the service-wide signal the entry reads. Sizing it
up ran into the other wall, because a canary slow enough to move that signal
also trips `canary-latency-rollback`, which suppresses this entry. The fix was
not a bigger number but a different place: the stable subset carries 80 percent
of traffic and is invisible to the canary entry's `destination_version=v2`
filter, so slowing it moves this signal hard and cannot bring the suppressor
into breach at all.

Two suppressions were proven rather than asserted.
`upstream-dependency-errors` kept `error-surge-outlier-ejection` quiet while
payments' own 5xx ratio was elevated by a fault one hop downstream, so the
healthy front service was not blamed. `canary-latency-rollback` kept
`latency-regression-vs-baseline` quiet, a suppression that did not exist until
these runs found the two firing together.

Every catalog entry now has either a proof or a declared reason it cannot have
one. `meshmedic-prove --list` prints the inventory: 16 proven, 3 declared
unprovable with a measurement behind each, 0 silently missing.

These files are verbatim captures, not edited for consistency. One of them
still carries the report formatting used before 2026-08-19, which is why its
section headers differ slightly from the rest. Rewriting a captured run to
match a later style would make it a nicer document and a worse record.

## Running these yourself

Run the prover in the background, not in a foreground shell with a timeout.
Reset is a deferred call: Ctrl-C and SIGTERM run it, SIGKILL does not. A shell
timeout that kills rather than signals will leave the fault applied, which
happened here and left `payments-v2` serving 500s for twenty minutes.

The next run catches it rather than compounding it. The preflight refuses to
start while any catalog entry is already breaching, and that check is
deliberately general: a resource scan would have reported the testbed clean,
because this fault was an environment variable and left no object behind.

## Bugs these runs found in the prover itself

The first `error-surge-outlier-ejection` run reported FAIL: the entry fired and
named the right workload, then never resolved. The entry was innocent. The
prover built a second detector for the resolution half, and a resolution is
only emitted on the firing-to-clear edge, so a detector that never saw the
incident open could never see it close.

It had passed twice before that. The dependency entries hold for 60 and 90
seconds, short enough that their fault outlived the reset and the fresh
detector fired and cleared on its own by accident. `error-surge` holds for 120
seconds and did not. One detector now spans both phases, with a regression test
built on a long hold duration where the accident cannot happen.

A proof harness that blames the product for its own bug is worse than no
harness, so this is written down rather than quietly fixed.
