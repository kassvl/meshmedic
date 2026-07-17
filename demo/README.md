# The 60 second demo

Storyboard for the M1 video. One take, no cuts, wall clock visible.

| t | shot | what happens |
| --- | --- | --- |
| 0-10s | terminal + Grafana split | demo mesh healthy: payments service, v1 stable, v2 canary at 20 percent |
| 10-20s | terminal | `./inject-canary-latency.sh` adds 1200ms delay to v2; Grafana p99 panel climbs |
| 20-35s | GitHub | MeshMedic PR appears: title, rendered VirtualService diff, PromQL evidence charts, narrative paragraph |
| 35-45s | GitHub | policy check green, human clicks merge |
| 45-60s | Grafana | Argo CD syncs, canary weight drops to 0, p99 falls back to baseline, panel green |

## Running it

The whole loop below has been exercised for real: chaos in, pull request
out, merge, mesh healed.

```console
$ ./scripts/00-cluster.sh          # kind cluster
$ ./scripts/01-istio.sh            # Istio ambient + Gateway API CRDs + Prometheus
$ ./scripts/02-app.sh              # payments v1/v2 + loadgen + waypoint
$ GITHUB_TOKEN=... ./scripts/03-argocd.sh   # Argo CD watching the config repo

$ kubectl -n istio-system port-forward svc/prometheus 9090:9090 &
$ GITHUB_TOKEN=... meshmedic watch --config ../demo/watch.yaml &

$ ./scripts/inject-canary-latency.sh
# ~2 minutes later MeshMedic opens the PR; merge it and watch
# the VirtualService shift to 100/0 and the canary drain to zero.
$ ./scripts/heal-canary.sh         # reset for the next take
```

Still missing for the video: a Grafana dashboard for the two panels in
shot 1, and the recording itself. The storyboard is the acceptance test:
if a step cannot be shown in its time slot, the component behind it is
not done.
