#!/usr/bin/env bash
# Chaos: give the canary a p50 latency far above the catalog threshold.
set -euo pipefail
kubectl -n demo set env deploy/payments-v2 TIMING_50_PERCENTILE=1200ms TIMING_90_PERCENTILE=1500ms
kubectl -n demo rollout status deploy/payments-v2 --timeout=120s
echo "canary is now slow; watch the p99 climb"
