#!/usr/bin/env bash
# Undo inject-canary-latency.sh.
set -euo pipefail
kubectl -n demo set env deploy/payments-v2 TIMING_50_PERCENTILE=20ms TIMING_90_PERCENTILE-
kubectl -n demo rollout status deploy/payments-v2 --timeout=120s
echo "canary latency restored"
