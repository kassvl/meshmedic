# Calibration: does this catalog stay quiet on a healthy cluster?

Three checks in this repository ask different questions about a catalog entry,
and it is worth being precise about which is which, because passing one says
nothing about the others.

| check | question | catches |
| --- | --- | --- |
| `catalog.lock` | is this the entry that was reviewed? | an entry edited after approval |
| `validate --against-prometheus` | do its metrics and labels exist here? | a renamed label, a metric nobody scrapes |
| `calibrate` | is its threshold right for **this** cluster? | an entry that fires when nothing is wrong |

An entry can be locked, resolve perfectly against Prometheus, and still open a
pull request every four minutes for a problem nobody has. That is the failure
this check exists for, and it is the one that gets a monitoring tool muted.

## Why silence is not a passing grade

The obvious version of this check is "run the catalog against an idle cluster
and require zero incidents". That check passes an entry whose healthy peak sits
at 199 against a threshold of 200. It fires the following Tuesday.

So `calibrate` measures **headroom**: how many times the healthy extreme must
change before it reaches the threshold.

```
greater-than entry:   headroom = threshold / observed maximum
less-than entry:      headroom = observed minimum / threshold
```

An entry at 65x headroom is calibrated. An entry at 1.35x stayed quiet during
the run and is one busy afternoon from firing. Both look identical to a check
that only counts incidents.

Two supporting decisions matter as much as the formula:

**The extreme, not the average.** A signal averaging 150ms that peaks at 250ms
will fire, and its average says it will not. Every number below is an extreme.

**Longest consecutive breach, against the entry's own `for` duration.** A
single 15-second excursion past the threshold does not fire an entry that
requires 90 seconds of holding. Reporting that as a failure would be a false
positive, and a gate that produces false positives is worth nothing, because
it will be the first thing anyone disables. So a brief crossing is reported as
`marginal`, and only a breach that held for the full duration is
`miscalibrated`. Two separate 40-second breaches are two breaches; they are
never summed into one 80-second breach that never happened.

## The verdicts

| verdict | meaning | what to do |
| --- | --- | --- |
| `calibrated` | quiet, with headroom above the margin factor | nothing |
| `marginal` | quiet, but close: either headroom under the factor, or it crossed the threshold without holding long enough to fire | widen the gap, or make the threshold relative with `baselineMultiplier` |
| `miscalibrated` | held past the threshold for its full hold duration on a healthy cluster | the entry fires on health; fix it before it reaches anyone |
| `unmeasured` | no sample resolved | see below, and do not read it as a pass |
| `not-applicable` | the signal needs a parameter this target does not define | nothing; the entry is about something else |

`marginal` and `unmeasured` both fail the gate. That is deliberate: the entire
reason this check exists is that "it did not fire today" is not the same claim
as "it is safe".

## `unmeasured` is the one to understand

An entry whose metric does not exist is perfectly silent. If silence were
scored, a dead entry would earn the best possible grade. So an entry that
produced no readings is never graded on its threshold, and an empty query
result is never folded in as a zero, because a zero looks like the safest
reading and would drag the observed extreme down.

There are three reasons an entry comes back `unmeasured`, and they need
different responses:

1. **The metric is not scraped here.** `validate --against-prometheus` says
   `metric missing`. The entry cannot fire at all; fix the entry or the
   scrape config.
2. **The selector matches nothing.** `validate --against-prometheus` says
   `selector matches nothing`. The metric exists but carries no series this
   entry can ever see.
3. **The failure simply is not happening.** This is the common case and it is
   not a defect. An entry selecting `response_code="403"` produces no series on
   a cluster where nothing is being denied.

Case 3 is the honest limit of this check, and it is a large limit: **a failure
threshold cannot be calibrated without the failure.** Measured on the project's
own testbed, 44 of 57 entry/target pairs fell into it. Those entries are
calibrated by injecting their fault, which is what the end-to-end suite does,
not by watching a healthy cluster.

## Running it

```console
$ meshmedic calibrate --config watch.yaml --window 10m
```

The cluster must actually be healthy for the result to mean anything: no
injected faults, no ongoing incident, and long enough after a restart that
caches are warm. That last point is not theoretical. On this project's own
testbed, an entry measured at 217-224ms against a 200ms threshold twenty
minutes after the cluster came back from twelve days off, and at 96.8ms once
it had settled. The first reading would have prompted a threshold change that
the second reading shows was never needed.

The window must comfortably exceed the catalog's longest hold duration, or an
entry cannot be shown to hold past its threshold and a `calibrated` verdict
would be a guess dressed as a measurement. The command refuses to run with a
window under three times the longest hold.

`--strict` exits non-zero unless every applicable entry is calibrated, for CI.

## Reading a real report

From the testbed, three targets, eight minutes, nothing injected:

```
ID                            TARGET               VERDICT        PEAK   THRESHOLD  HEADROOM
canary-latency-rollback       orders.demo-sidecar  miscalibrated  1450   > 1000     0.69x
canary-latency-rollback       payments.demo        marginal       740    > 1000     1.35x
upstream-dependency-latency   payments.demo        calibrated     96.78  > 200      2.07x
waypoint-overload-scale       payments.demo        calibrated     15.35  > 1000     65.1x
traffic-vanished-triage       payments.demo        calibrated     0      > 0.5      inf
```

The first row is a real finding: sidecar-mode `orders` is slower than the
ambient service the 1000ms threshold was chosen against, so the entry held past
it for 1m45s, its full hold duration, on a cluster with nothing wrong. The
second row is the same entry on the ambient service, quiet but with only 1.35x
of room. The remedy for both is the same and it is in the catalog already:
`baselineMultiplier` makes an entry fire on deviation from a target's own
learned normal instead of a fixed number that is right for one cluster and
wrong for every other.
