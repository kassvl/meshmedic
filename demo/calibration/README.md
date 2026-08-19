# Calibration gate: live run

Eight minutes against the kind + Istio 1.24.1 testbed with nothing injected,
three targets: `payments.demo` (ambient), `orders.demo-sidecar` (sidecar mode),
and `ghost.nonexistent` (a namespace that does not exist, included so the
report has a known-unobservable control in it).

Full output in [`live-run.txt`](live-run.txt). Method and how to read a
verdict: [`docs/calibration.md`](../../docs/calibration.md).

## What it found

| verdict | count |
| --- | --- |
| calibrated | 7 |
| marginal | 2 |
| miscalibrated | 1 |
| unmeasured | 44 |
| not applicable | 3 |

**One real miscalibration.** `canary-latency-rollback` on
`orders.demo-sidecar`: peak 1450ms against a 1000ms threshold, held for 1m45s,
which is its full hold duration. The entry fires on a healthy cluster. The
threshold was chosen against the ambient `payments` service; sidecar-mode
`orders` is simply slower, and nothing before this check would have said so.

**Two marginal.** `canary-latency-rollback` and
`latency-regression-vs-baseline` on `payments.demo`, both at 740ms against
1000ms: 1.35x headroom. Quiet during the run, and one busy afternoon from
firing. A gate that only counted incidents would have passed both.

**One correction this run produced.** `upstream-dependency-latency` on
`payments.demo` measured 217-224ms against its 200ms threshold twenty minutes
after the cluster returned from twelve days powered off, and fired. Eight
minutes of measurement once it had settled put it at 96.78ms with 2.07x
headroom. The first reading would have prompted a threshold change the second
shows was never needed, which is why the documentation insists the cluster be
warm before the result is believed.

## The limit this run made concrete

44 of 57 entry/target pairs came back `unmeasured`, almost all of them for the
same reason: an entry selecting `response_code="403"` or
`response_flags="UO"` produces no series on a cluster where nothing is being
denied and nothing is overflowing.

That is not a defect in the check. It is the honest boundary of what watching
a healthy cluster can establish: **a failure threshold cannot be calibrated
without the failure.** Those entries are calibrated by injecting their fault
and measuring, not by observing health.
