#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
go_root="$(cd -- "$script_dir/.." && pwd)"
repo_root="$(cd -- "$go_root/.." && pwd)"
frontend_dist="$repo_root/frontend/dist"
embedded_dist="$go_root/web/dist"
output="${1:-$go_root/dist/viewerd}"

npm --prefix "$repo_root/frontend" run build
mkdir -p "$embedded_dist" "$(dirname -- "$output")"
if command -v rsync >/dev/null 2>&1; then
  rsync -a --delete "$frontend_dist/" "$embedded_dist/"
else
  find "$embedded_dist" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
  cp -R "$frontend_dist/." "$embedded_dist/"
fi
(cd -- "$go_root" && go build -o "$output" ./cmd/viewerd)
printf 'built %s\n' "$output"
