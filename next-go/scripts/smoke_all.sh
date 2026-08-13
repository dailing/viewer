#!/usr/bin/env bash
# 黑盒总验收：全部 Python 冒烟套件打 Go 二进制。
# 用法: scripts/smoke_all.sh  （在 next-go/ 或仓库任意目录运行）
set -uo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
NEXT_GO=$(cd -- "$SCRIPT_DIR/.." && pwd)
ROOT=$(cd -- "$NEXT_GO/.." && pwd)
PY=${VIEWER_PYTHON:-$ROOT/next/.venv/bin/python}
export VIEWER_SMOKE_NEXT=$ROOT/next
export PATH="$HOME/.local/go/bin:$PATH"

LOG=${SMOKE_LOG_DIR:-/tmp/viewer-smoke-all}
rm -rf "$LOG"; mkdir -p "$LOG"
BIN=$LOG/bin; mkdir -p "$BIN"

echo "== building binaries =="
cd "$NEXT_GO"
for b in viewerd viewer-terminal viewer-gateway viewer-configstore \
         viewer-instancestore viewer-fileservice viewer-supervisor viewer-inspector; do
  go build -o "$BIN/$b" "./cmd/$b" || { echo "BUILD FAIL $b"; exit 1; }
done
export VIEWERD_BIN=$BIN/viewerd
export VIEWER_SUPERVISOR_BIN=$BIN/viewer-supervisor
export VIEWER_INSPECTOR_BIN=$BIN/viewer-inspector
export VIEWER_CONFIGSTORE_BIN=$BIN/viewer-configstore
export VIEWER_INSTANCESTORE_BIN=$BIN/viewer-instancestore
export VIEWER_FILESERVICE_BIN=$BIN/viewer-fileservice

PASS=0; FAIL=0
run_suite() {
  local name=$1; shift
  echo "=== $name ==="
  if timeout 240 "$@" > "$LOG/$name.log" 2>&1; then
    echo "PASS $name"; PASS=$((PASS+1))
  else
    echo "FAIL $name (rc=$?, log: $LOG/$name.log)"; FAIL=$((FAIL+1))
  fi
}

wait_port() {
  local port=$1
  for _ in $(seq 1 60); do
    (echo > "/dev/tcp/127.0.0.1/$port") 2>/dev/null && return 0
    sleep 0.1
  done
  echo "port $port never came up"; return 1
}

PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null; done; }
trap cleanup EXIT

# 1. kernel: 主体用例打 28766 上的外部内核（内核独占模式）；4009 用例自拉起子内核
"$BIN/viewerd" --plugins=none --kernel-port 28766 --data-dir "$LOG/data-28766" > "$LOG/viewerd-28766.log" 2>&1 &
PIDS+=($!)
wait_port 28766 || exit 1
run_suite kernel "$PY" "$SCRIPT_DIR/smoke_kernel.py"
kill "${PIDS[-1]}" 2>/dev/null; unset 'PIDS[-1]'

# 2. 共享内核 29430（内核独占）: stores / fileservice / terminal
"$BIN/viewerd" --plugins=none --kernel-port 29430 --data-dir "$LOG/data-29430" > "$LOG/viewerd-29430.log" 2>&1 &
PIDS+=($!)
wait_port 29430 || exit 1

run_suite stores "$PY" "$SCRIPT_DIR/smoke_stores.py"
run_suite fileservice "$PY" "$SCRIPT_DIR/smoke_fileservice.py"

"$BIN/viewer-terminal" --kernel-ws ws://127.0.0.1:29430/ws > "$LOG/terminal-plugin.log" 2>&1 &
PIDS+=($!)
sleep 1.5
run_suite terminal "$PY" "$SCRIPT_DIR/smoke_terminal.py" --kernel-ws ws://127.0.0.1:29430/ws
kill "${PIDS[-1]}" 2>/dev/null; unset 'PIDS[-1]'
kill "${PIDS[-1]}" 2>/dev/null; unset 'PIDS[-1]'

# 3. supervisor / inspector（各自拉起内核，端口 29371/29372）
run_suite supervisor "$PY" "$SCRIPT_DIR/smoke_supervisor.py"
run_suite inspector "$PY" "$SCRIPT_DIR/smoke_inspector.py"

# 4. chat 单独拉起按需白名单 viewerd，并以 mock ACP agent 验证 turn/DB/cancel。
run_suite chat "$PY" "$SCRIPT_DIR/smoke_chat.py"

# 5. gateway 最后跑——smoke 会 SIGTERM 内核验证关闭传导
STATIC=$(mktemp -d)
printf '<html><body>viewer-static</body></html>\n' > "$STATIC/index.html"
printf 'body{color:red}\n' > "$STATIC/style.css"
"$BIN/viewerd" --plugins=none --kernel-port 29200 --data-dir "$LOG/data-29200" > "$LOG/viewerd-29200.log" 2>&1 &
K29200=$!
wait_port 29200 || exit 1
"$BIN/viewer-gateway" --kernel-ws ws://127.0.0.1:29200/ws --port 29201 --static "$STATIC" > "$LOG/gateway-plugin.log" 2>&1 &
GW=$!
sleep 1.5
run_suite gateway "$PY" "$SCRIPT_DIR/smoke_gateway.py" \
  --kernel-ws ws://127.0.0.1:29200/ws --kernel-pid "$K29200" --static-dir "$STATIC"
kill "$GW" "$K29200" 2>/dev/null
rm -rf "$STATIC"

# 6. 单二进制装配冒烟（M4 落地后存在则跑）
if [ -f "$SCRIPT_DIR/smoke_single_binary.py" ]; then
  run_suite single-binary "$PY" "$SCRIPT_DIR/smoke_single_binary.py" --viewerd-bin "$BIN/viewerd"
fi

echo "================================"
echo "RESULT: $PASS pass, $FAIL fail   (logs: $LOG)"
[ "$FAIL" -eq 0 ]
