## Incident: Traffic to a service vanished (clients went silent)

Scenario `traffic-vanished-triage` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **1** (threshold > 0.5 for 60s) since 2026-08-19T13:48:23Z.

### Diagnosis

The service was receiving steady traffic ten minutes ago and now receives none, while its own pods stay Running. This is not a mesh fault signature; it is the absence of one, and the cause usually sits one layer above the mesh: a bad client rollout, a broken target hostname, a dead upstream caller. No patch can be proposed automatically because the failing party is not the service itself. Instead this scenario assembles a triage dossier: who used to call the service, which workloads in the namespace are logging known failure signatures right now, and what rolled out recently with the exact template diff. The root cause of a bad deploy is usually a line in that diff.

### Evidence

| query | value |
| --- | --- |
| former-callers-by-source{source_workload="loadgen", source_workload_namespace="demo"} | 3.449 |

### Log evidence

**client-failure-log-sweep** — `loadgen`:

```
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
[pod/loadgen-6b47549665-rrdbt/loadgen] curl: (6) Could not resolve host: payments-svc.demo
```

### Recent rollouts

**recent-rollouts** — `loadgen` rolled 211s ago, template diff (previous → current):

```diff
- "pod-template-hash": "58bf55cf44"
- "while true; do curl -s -o /dev/null http://payments:9090/; sleep 0.2; done"
+ "pod-template-hash": "6b47549665"
+ "while true; do curl -sS -m 2 -o /dev/null http://payments-svc.demo:9090/; sleep 0.2; done"
```
**recent-rollouts** — `payments-v2` rolled 1672s ago, template diff (previous → current):

```diff
- "pod-template-hash": "6f668ccd94",
- "value": "0.9"
- "name": "ERROR_CODE",
- "value": "500"
+ "pod-template-hash": "6bf6c4fd55",
+ "value": "0"
```

### Proposed patch (Deployment)

```yaml
# report-only scenario: no patch is proposed.
# The evidence above is the deliverable; act on it by hand.
```

### Rollback

Not applicable: nothing is applied. If the recent-rollouts diff shows a suspicious change in a calling workload, `kubectl rollout undo` on that deployment is the usual first move; the dossier names the deployment and the changed lines so the operator can decide in one look.

This scenario requires human approval; nothing was applied to the cluster.
