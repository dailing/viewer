#!/usr/bin/env bash
# Bring up the viewer-next stack: kernel + C0 supervisor (which spawns every
# enabled plugin in the registry). Ctrl-C stops the whole stack gracefully:
# supervisor first (it SIGTERMs its managed plugins via on_stop), then the
# kernel (4009 close drains anything left).
#
# Env overrides:
#   VIEWER_HOST      kernel bind host   (default 127.0.0.1)
#   VIEWER_PORT      kernel port        (default 18765)
#   VIEWER_REGISTRY  registry JSON      (default registry.json)
#   VIEWER_HTTP_*    see plugins/gateway/backend/run

set -euo pipefail
cd "$(dirname "$0")"

HOST="${VIEWER_HOST:-127.0.0.1}"
PORT="${VIEWER_PORT:-18765}"
REGISTRY="${VIEWER_REGISTRY:-registry.json}"
PY=.venv/bin/python

KERNEL_PID=""
SUPERVISOR_PID=""

shutdown() {
  echo "[start] shutting down..."
  if [ -n "$SUPERVISOR_PID" ]; then
    kill "$SUPERVISOR_PID" 2>/dev/null || true
    wait "$SUPERVISOR_PID" 2>/dev/null || true
  fi
  if [ -n "$KERNEL_PID" ]; then
    kill "$KERNEL_PID" 2>/dev/null || true
    wait "$KERNEL_PID" 2>/dev/null || true
  fi
  echo "[start] stack stopped"
  exit 0
}
trap shutdown INT TERM

echo "[start] kernel ws://$HOST:$PORT/ws"
"$PY" -m kernel --host "$HOST" --port "$PORT" &
KERNEL_PID=$!

# Wait for the kernel port to accept connections (max ~10s).
READY=0
for _ in $(seq 1 100); do
  if (echo > "/dev/tcp/$HOST/$PORT") 2>/dev/null; then
    READY=1
    break
  fi
  if ! kill -0 "$KERNEL_PID" 2>/dev/null; then
    echo "[start] kernel exited during startup" >&2
    exit 1
  fi
  sleep 0.1
done
if [ "$READY" != "1" ]; then
  echo "[start] kernel did not open $HOST:$PORT within 10s" >&2
  shutdown
fi

echo "[start] supervisor (registry: $REGISTRY)"
"$PY" -m plugins.supervisor --kernel-ws "ws://$HOST:$PORT/ws" --registry "$REGISTRY" &
SUPERVISOR_PID=$!

# If either process exits, bring the whole stack down.
wait -n "$KERNEL_PID" "$SUPERVISOR_PID" || true
shutdown
