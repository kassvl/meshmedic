# Security

## What this tool can reach

Being precise about this is more useful than a policy statement, because the
answer is most of the security story.

**The detector holds no cluster write credentials.** It reads Prometheus over
HTTP, and it reads Kubernetes objects, pod logs and rollout history by shelling
out to `kubectl get` and `kubectl logs`. There is no code path in it that
creates, updates or deletes anything in a cluster. Its output is a pull request
in a configuration repository, which your existing review and policy checks then
gate. That is a structural property rather than a setting, so there is nothing
to misconfigure into a cluster write.

**It reads whatever your kubeconfig can read.** Give it a context scoped to the
namespaces you want it to see. `MESHMEDIC_KUBE_CONTEXT` pins which context it
uses, which also prevents the failure where reads land on a different cluster
than the one you believe you are watching.

**A GitHub token is optional and only used for pull requests.** Set
`MESHMEDIC_GITHUB_TOKEN` or `GITHUB_TOKEN` only if you configure the `gitops`
section. It needs write access to the configuration repository and nothing
else, and it is deliberately reachable only from a config file rather than a
flag, so it cannot be enabled by pasting a command out of a README. Without
it, incidents are printed and nothing leaves the process.

**Incident reports contain cluster data.** Labeled metrics, object fields you
selected as evidence, and matched log lines all end up in the report, and
therefore in the pull request body. Log evidence is filtered to lines matching
the entry's curated patterns rather than dumped wholesale, but treat a report
as carrying whatever your telemetry and logs carry.

## meshmedic-prove is different, on purpose

`meshmedic-prove` **injects faults into the cluster you point it at**. It
applies policies, patches routes, scales workloads to zero and slows services,
and it exists so that anyone can reproduce the end-to-end evidence for a catalog
entry rather than taking a maintainer's word for it.

It ships as a separate binary and a separate download, is absent from the
container image, prints every command it will run before running any of them,
and refuses to start without `--yes-inject-faults`. It resets what it injected,
including after a failure and after Ctrl-C, but a `SIGKILL` cannot run that
cleanup, so an interrupted run can leave a fault applied. The next run's
preflight refuses to start while any catalog entry is already breaching, which
is how that gets caught.

Run it against a testbed. Never against anything you care about.

## Container image

Built on distroless, runs as `nonroot`, and contains the detector only. The
prover and `kubectl` are both deliberately absent: nothing that runs
continuously inside a mesh should carry a fault-injection tool, and a compromised
pod should not find a cluster CLI waiting for it. Configuration and triage
evidence degrade to a logged line without `kubectl`, and metric detection is
unaffected.

## Reporting a vulnerability

Open a [security advisory](https://github.com/kassvl/meshmedic/security/advisories/new)
rather than a public issue. If you would rather email, the address on the commits
in this repository reaches the maintainer.

This is a single-maintainer project, so please do not expect a same-day reply.
You will get an acknowledgement and an honest estimate rather than silence.
