#!/usr/bin/env bash
# Reset the environment for a fresh demo take: drop merged remediation files
# from the config repo, restore the 80/20 split, heal the canary. Needs gh.
set -euo pipefail
cd "$(dirname "$0")/.."

REPO="kassvl/meshmedic-demo-config"
for f in $(gh api "repos/$REPO/contents/istio/demo" --jq '.[] | select(.name != ".gitkeep") | .path'); do
  sha=$(gh api "repos/$REPO/contents/$f" --jq .sha)
  gh api -X DELETE "repos/$REPO/contents/$f" \
    -f message="demo: reset take" -f sha="$sha" >/dev/null
  echo "removed $f from config repo"
done

kubectl apply -f manifests/payments.yaml >/dev/null
./scripts/heal-canary.sh
echo "take reset: traffic 80/20, canary healthy"
