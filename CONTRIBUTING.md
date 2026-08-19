# Contributing

The most useful thing you can contribute is a catalog entry, and the process
for one is unusual enough to be worth explaining before you start.

## The rule that shapes everything else

No entry merges on the strength of the PromQL looking right. It merges when
the fault has been injected on a real mesh and the signal has been watched to
appear. That discipline has repeatedly caught what specifications do not tell
you: that a no-route request is stamped `destination_service_name=unknown` and
so must be attributed to the source, that a wrong-port client logs "Empty reply
from server" rather than "connection refused", and that a rolled-back
Deployment reuses its ReplicaSet, which defeats age-based rollout detection.

One entry was retired outright after measurement showed the telemetry it reads
does not exist in the data plane it targets. See
[`catalog/RETIRED.md`](catalog/RETIRED.md). Reading it is the fastest way to
understand what the bar is.

## Adding an entry

**1. Write the entry.** Plain YAML in [`catalog/`](catalog/). Copy the closest
existing one; the fields are documented in
[`pkg/catalog/types.go`](pkg/catalog/types.go). Every entry needs a signal, a
rollback note, and guardrails. `report-only` is the right action whenever the
correct fix depends on intent rather than mechanics.

**2. Check its metrics exist.**

```console
$ meshmedic validate --against-prometheus http://localhost:9090
```

This parses your PromQL, extracts the metric names and label keys, and confirms
each one resolves on a live server. It reports `metric missing`,
`label missing`, or `selector matches nothing`, the last being the case where
every name exists and the selector still matches no series. That distinction
matters: a metric can be present and carry nothing your entry can ever see.

**3. Check it stays quiet when nothing is wrong.**

```console
$ meshmedic calibrate --config watch.yaml --window 10m
```

Run this against a healthy cluster. It measures headroom, not silence: an entry
whose healthy peak sits just under its threshold is quiet today and pages next
Tuesday, and a check that only counts incidents cannot tell the two apart.
`marginal` fails, and so does `unmeasured`. The reasoning is in
[`docs/calibration.md`](docs/calibration.md).

**4. Write its proof.** A declarative spec in [`proof/`](proof/) that injects
the fault, and asserts the entry fires inside its own hold duration, names the
culprit in the report, keeps its declared neighbours quiet, and clears on
reset. Copy a neighbour; the fields are in
[`pkg/proof/spec.go`](pkg/proof/spec.go).

Two things trip people up. Reset commands must be idempotent, because reset
runs twice on a passing proof: once to prove the incident closes and once in
the deferred cleanup. And proving that something fired without proving it named
the culprit proves the entry detected something and explained nothing, which is
why `expect.names` is required.

**5. Run it.**

```console
$ meshmedic-prove --entry your-entry --kube-context <ctx> --yes-inject-faults
```

`meshmedic-prove` injects faults into whatever cluster you point it at. It is a
separate binary and a separate download for that reason, it prints every
command before running any of them, and it refuses to start without the
acknowledgement flag. Point it at a testbed.

If your entry genuinely cannot be proven where you are, say so in
[`proof/UNPROVABLE.yaml`](proof/UNPROVABLE.yaml) with the measurement behind
the claim and what would have to change. An entry with no proof and no
declaration is the ambiguous kind: nobody can tell whether it has not been got
to yet or cannot be done.

**6. Attach the evidence.** Put the incident report your run produced in the
pull request. That is the review; the YAML is just how it is written down.

Approval is a separate, human step, and deliberately not part of the pull
request: a maintainer runs `meshmedic approve` after seeing the run, which
records the entry's content hash along with the Istio version and testbed
commit it was validated against. From then on, editing the entry stops it from
running until it is re-validated and re-approved.

## Running the checks locally

Everything CI runs, in the order it runs it:

```console
$ go build ./... && go vet ./... && go test ./...
$ go run ./cmd/meshmedic validate
$ go run ./cmd/meshmedic validate --no-drift
```

The last one is the gate that fails when an approved entry was edited, or when
an entry was removed without its approval being retired. Go 1.21 or newer.

## Contributing a benchmark scenario instead

If what you have is a real Istio incident rather than a detector for one, it
belongs in [mesh-incidents-bench](https://github.com/kassvl/mesh-incidents-bench),
and it is worth more there. That repository scores any tool against reproducible
mesh failures, and its honest limit is that its author also wrote MeshMedic.
Scenarios written by somebody else are the fix for that, and they are welcome
even when this tool does badly on them. Especially then.

## What is out of scope

A few things are settled and not open for debate in a pull request, so that
nobody spends a weekend on something that will be declined:

- **No LLM in the detection or remediation path.** This is the product's
  identity rather than a stage it is growing out of.
- **No generic Kubernetes breadth.** That lane belongs to other tools.
- **No cluster writes.** The detector holds no write credentials and its output
  is a pull request. Anything that needs `kubectl apply` lives in the prover.
