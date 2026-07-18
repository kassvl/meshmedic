#!/usr/bin/env bash
# Deploy the demo app and enroll the ambient namespace behind a waypoint (which
# is what gives ambient mode its L7 per-version, per-request metrics). The app
# is a payments service (v1 stable, v2 canary) that calls a downstream ledger
# dependency, fronted by a north-south ingress Gateway, plus a sidecar-mode
# orders service in demo-sidecar so the catalog can be exercised in both data
# planes.
set -euo pipefail
cd "$(dirname "$0")/.."

# payments.yaml creates the demo namespace; ledger lands in it. payments calls
# ledger on every request, so both are brought up together and waited on.
kubectl apply -f manifests/payments.yaml
kubectl apply -f manifests/ledger.yaml
kubectl -n demo rollout status deploy/ledger --timeout=300s
kubectl -n demo rollout status deploy/payments-v1 --timeout=300s
kubectl -n demo rollout status deploy/payments-v2 --timeout=300s
kubectl -n demo rollout status deploy/loadgen --timeout=300s

istioctl waypoint apply -n demo --enroll-namespace --wait

# North-south ingress Gateway fronting payments (Istio auto-provisions the pod)
# with its own loadgen for continuous edge traffic.
kubectl apply -f manifests/ingress.yaml
kubectl -n demo rollout status deploy/ingress-loadgen --timeout=300s

# Sidecar-mode counterpart (demo-sidecar) for validating the mode-agnostic
# entries in classic sidecar mode as well as ambient.
kubectl apply -f manifests/sidecar-orders.yaml
kubectl -n demo-sidecar rollout status deploy/orders-v1 --timeout=300s
kubectl -n demo-sidecar rollout status deploy/orders-v2 --timeout=300s
kubectl -n demo-sidecar rollout status deploy/loadgen --timeout=300s

kubectl -n demo get pods -o wide
kubectl -n demo-sidecar get pods -o wide
