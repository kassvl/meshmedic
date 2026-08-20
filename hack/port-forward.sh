#!/usr/bin/env bash
#
# Keep a Prometheus port-forward alive for the length of a proof run.
#
# This is a shell script rather than something the prover owns, because a
# port-forward is an operator's problem and a tool that quietly manages one
# hides the fact that it needs it. But it is in the repo rather than in
# somebody's scrollback, because it was written from scratch twice in one
# session and both versions were wrong in a way worth recording.
#
# The first version restarted immediately. `kubectl port-forward` does not
# release the listening socket the instant it exits, so the restart lost the
# race against its own predecessor and printed "address already in use" for
# thirty seconds. A one-second gap became a thirty-second one, which is long
# enough for a detector to report blind and for a harness to blame an entry.
# Waiting for the port to drain before rebinding is the whole fix.
#
# Usage:
#   hack/port-forward.sh [context] [namespace] [service] [port]
#
# It runs until interrupted. Pair it with `meshmedic-prove doctor` to confirm
# the forward reaches the cluster you think it does: a live forward is not
# evidence of that, because forwards outlive the cluster they were opened
# against.
set -uo pipefail

CONTEXT="${1:-kind-meshmedic-demo}"
NAMESPACE="${2:-istio-system}"
SERVICE="${3:-prometheus}"
PORT="${4:-9090}"

echo "forwarding ${CONTEXT} ${NAMESPACE}/${SERVICE}:${PORT}, restarting on drop; interrupt to stop" >&2

port_is_free() {
  # lsof is present on macOS and most Linux images; if it is missing, fall
  # back to a short fixed wait rather than spinning forever on a check that
  # cannot answer.
  command -v lsof >/dev/null 2>&1 || { sleep 2; return 0; }
  ! lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1
}

while true; do
  # Wait for the previous listener to let go. Bounded, so a genuinely
  # occupied port surfaces as a bind error instead of a silent hang.
  for _ in $(seq 1 60); do
    port_is_free && break
    sleep 1
  done

  kubectl --context="$CONTEXT" -n "$NAMESPACE" port-forward "svc/$SERVICE" "$PORT:$PORT"
  echo "$(date -u +%H:%M:%SZ) forward exited; waiting for the port to drain, then restarting" >&2
done
