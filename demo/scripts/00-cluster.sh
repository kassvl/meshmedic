#!/usr/bin/env bash
# Create the kind cluster for the demo.
set -euo pipefail
cd "$(dirname "$0")/.."

kind create cluster --config kind/cluster.yaml
kubectl cluster-info --context kind-meshmedic-demo
