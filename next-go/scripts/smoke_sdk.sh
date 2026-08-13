#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
NEXT_GO_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)

export PATH="$HOME/.local/go/bin:$PATH"
cd "$NEXT_GO_DIR"
go build -o /tmp/viewerd ./cmd/viewerd

echo "=== busclient smoke: go kernel ==="
BUSCLIENT_SMOKE_KERNEL=go go test ./internal/busclient -run '^TestSDKSmokeDualKernel$' -count=1 -v
