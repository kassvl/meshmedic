#!/usr/bin/env bash
# Deploy the payments demo app and enroll the namespace behind a waypoint,
# which is what gives ambient mode its L7 (per-version, per-request) metrics.
set -euo pipefail
cd "$(dirname "$0")/.."

kubectl apply -f manifests/payments.yaml
kubectl -n demo rollout status deploy/payments-v1 --timeout=300s
kubectl -n demo rollout status deploy/payments-v2 --timeout=300s
kubectl -n demo rollout status deploy/loadgen --timeout=300s

istioctl waypoint apply -n demo --enroll-namespace --wait
kubectl -n demo get pods -o wide
