#!/usr/bin/env bash
# Install the Istio Grafana addon and import the demo dashboard.
set -euo pipefail
cd "$(dirname "$0")/.."

ISTIO_VERSION="$(istioctl version --remote=false -o json | sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' | head -1)"
ISTIO_MINOR="release-$(echo "$ISTIO_VERSION" | cut -d. -f1-2)"

kubectl apply -f "https://raw.githubusercontent.com/istio/istio/${ISTIO_MINOR}/samples/addons/grafana.yaml"
kubectl -n istio-system rollout status deploy/grafana --timeout=300s

kubectl -n istio-system port-forward svc/grafana 3000:3000 >/dev/null 2>&1 &
PF_PID=$!
trap 'kill $PF_PID 2>/dev/null' EXIT
until curl -sf http://localhost:3000/api/health >/dev/null; do sleep 2; done

DS_UID="$(curl -sf http://localhost:3000/api/datasources | python3 -c 'import json,sys; print([d["uid"] for d in json.load(sys.stdin) if d["type"]=="prometheus"][0])')"
python3 - "$DS_UID" <<'EOF' | curl -sf -X POST http://localhost:3000/api/dashboards/db -H 'Content-Type: application/json' -d @- | python3 -m json.tool
import json, sys
with open("grafana-dashboard.json") as f:
    dashboard = json.load(f)
dashboard_str = json.dumps(dashboard).replace("DS_UID", sys.argv[1])
print(json.dumps({"dashboard": json.loads(dashboard_str), "overwrite": True}))
EOF

echo "dashboard imported: http://localhost:3000/d/meshmedic-demo"
