#!/usr/bin/env bash

set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

GOBIN="$work_dir/bin" go install github.com/google/go-licenses@v1.6.0
license_tool="$work_dir/bin/go-licenses"
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)

for target in "${targets[@]}"; do
  goos=${target%/*}
  goarch=${target#*/}
  ignore_args=(--ignore=github.com/weavegate/weavegate)

  while IFS= read -r package; do
    ignore_args+=("--ignore=$package")
  done < <(GOOS="$goos" GOARCH="$goarch" go list std)

  GOOS="$goos" GOARCH="$goarch" "$license_tool" report \
    --template "$repo_root/.go-licenses-notice.tpl" \
    "${ignore_args[@]}" ./cmd/weavegate > "$work_dir/$goos-$goarch.csv"
done

{
  printf '%s\n' \
    'weavegate' \
    'Copyright 2026 weavegate contributors' \
    '' \
    'This product includes the union of third-party runtime dependencies for' \
    'linux/amd64, linux/arm64, darwin/amd64, and darwin/arm64. Regenerate this' \
    'inventory from the repository root with:' \
    '' \
    './scripts/gen-notice.sh' \
    '' \
    'Go standard library packages and the weavegate module itself are excluded.' \
    '' \
    'Package,License,License source'
  sort -u "$work_dir"/*.csv
} > "$repo_root/NOTICE"
