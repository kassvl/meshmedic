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

| entry | fired after | named | resolved after |
| --- | --- | --- | --- |
| `upstream-dependency-errors` | 1m5s | `ledger` at 3.708 rps | 1m45s |
| `upstream-dependency-latency` | 1m30s | `ledger` | 2m5s |
| `error-surge-outlier-ejection` | 2m3s | `payments-v2` | 42s |

`upstream-dependency-errors` also demonstrated its suppression working:
`error-surge-outlier-ejection` stayed quiet while payments' own 5xx ratio was
elevated by a fault one hop downstream, so the healthy front service was not
blamed.

## A bug these runs found in the prover itself

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
