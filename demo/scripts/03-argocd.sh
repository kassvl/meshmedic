#!/usr/bin/env bash
# Install Argo CD and point it at the MeshMedic config repo, so merging a
# MeshMedic pull request syncs the patch into the mesh. Needs GITHUB_TOKEN
# (or MESHMEDIC_GITHUB_TOKEN) with read access to the config repo.
set -euo pipefail
cd "$(dirname "$0")/.."

TOKEN="${MESHMEDIC_GITHUB_TOKEN:-${GITHUB_TOKEN:?set GITHUB_TOKEN or MESHMEDIC_GITHUB_TOKEN}}"

kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
# Server-side apply: the applicationsets CRD exceeds the annotation size
# limit that client-side apply relies on.
kubectl apply -n argocd --server-side \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
kubectl -n argocd rollout status deploy/argocd-repo-server deploy/argocd-server --timeout=300s
kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=300s

kubectl -n argocd create secret generic meshmedic-demo-config-repo \
  --from-literal=type=git \
  --from-literal=url=https://github.com/kassvl/meshmedic-demo-config \
  --from-literal=username=x-access-token \
  --from-literal=password="$TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n argocd label secret meshmedic-demo-config-repo \
  argocd.argoproj.io/secret-type=repository --overwrite

kubectl apply -f manifests/argocd-app.yaml
echo "argocd is watching the config repo; merged MeshMedic PRs will sync"
