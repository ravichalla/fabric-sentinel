#!/bin/sh
# End-to-end demo of the agent in simulator mode.
# Four simulated links follow different scenarios; after ~12s the script prints
# the per-link verdicts and the matching Prometheus series.
set -eu

PORT="${PORT:-9101}"
BIN="${BIN:-./bin/agent}"
ADDR="127.0.0.1:${PORT}"

"$BIN" -mode sim -node demo-node -listen "$ADDR" -interval 500ms -sim-step 5s &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT INT TERM

echo "Agent started (pid $PID). Letting simulated time run for ~12s..."
sleep 12

echo
echo "== /status (links and verdicts) =="
curl -fsS "http://${ADDR}/status" | grep -E '"(node|state|worst_link|link|score|flaps)"'

echo
echo "== /metrics (health score per link, and which state is active) =="
curl -fsS "http://${ADDR}/metrics" \
  | grep -E '^fabric_sentinel_(link_health_score|link_state\{.*\} 1$|node_health_score)'

echo
echo "== /healthz =="
curl -fsS "http://${ADDR}/healthz"
