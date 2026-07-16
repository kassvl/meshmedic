# The 60 second demo

Storyboard for the M1 video. One take, no cuts, wall clock visible.

| t | shot | what happens |
| --- | --- | --- |
| 0-10s | terminal + Grafana split | demo mesh healthy: payments service, v1 stable, v2 canary at 20 percent |
| 10-20s | terminal | `./inject-canary-latency.sh` adds 1200ms delay to v2; Grafana p99 panel climbs |
| 20-35s | GitHub | MeshMedic PR appears: title, rendered VirtualService diff, PromQL evidence charts, narrative paragraph |
| 35-45s | GitHub | policy check green, human clicks merge |
| 45-60s | Grafana | Argo CD syncs, canary weight drops to 0, p99 falls back to baseline, panel green |

## Environment (to build)

- kind cluster, Istio ambient profile
- demo app: two-version payments service behind a VirtualService
- kube-prometheus-stack, Grafana dashboard tuned for the two panels in shot 1
- Argo CD watching a local gitea or a scratch GitHub repo, so the merge visibly syncs
- chaos scripts: `inject-canary-latency.sh` (fault injection via EnvoyFilter or app flag), `inject-5xx.sh`, `revoke-mtls-client.sh`

Scripts land in this directory as they are written. The storyboard is the
acceptance test: if a step cannot be shown in its time slot, the component
behind it is not done.
