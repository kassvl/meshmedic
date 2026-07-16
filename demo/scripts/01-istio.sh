#!/usr/bin/env bash
# Install Istio in ambient mode plus the Prometheus addon.
set -euo pipefail

ISTIO_VERSION="$(istioctl version --remote=false -o json | sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' | head -1)"
ISTIO_MINOR="release-$(echo "$ISTIO_VERSION" | cut -d. -f1-2)"

# Ambient waypoints are Gateway API resources; kind does not ship the CRDs.
kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml"

istioctl install --set profile=ambient --skip-confirmation

kubectl apply -f "https://raw.githubusercontent.com/istio/istio/${ISTIO_MINOR}/samples/addons/prometheus.yaml"
kubectl -n istio-system rollout status deploy/prometheus --timeout=180s
