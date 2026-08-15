#!/usr/bin/env bash
# Release build: build the real frontend, sync it into the embed tree,
# compile viewerd (go:embed picks the assets up at compile time), then restore
# web/dist to its committed placeholder state — real assets are build artifacts
# and must never dirty the working tree.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
frontend_dist="$repo_root/frontend/dist"
embedded_dist="$repo_root/web/dist"
output="${1:-$repo_root/dist/viewerd}"

backup="$(mktemp -d)"
cp -R "$embedded_dist/." "$backup/"
restore_embed() {
  find "$embedded_dist" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
  cp -R "$backup/." "$embedded_dist/"
  rm -rf "$backup"
}
trap restore_embed EXIT

npm --prefix "$repo_root/frontend" run build
mkdir -p "$embedded_dist" "$(dirname -- "$output")"
if command -v rsync >/dev/null 2>&1; then
  rsync -a --delete --exclude=.gitignore "$frontend_dist/" "$embedded_dist/"
else
  find "$embedded_dist" -mindepth 1 -maxdepth 1 ! -name .gitignore -exec rm -rf -- {} +
  cp -R "$frontend_dist/." "$embedded_dist/"
fi
(cd -- "$repo_root" && go build -o "$output" ./cmd/viewerd)
printf 'built %s\n' "$output"
