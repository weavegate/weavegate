#!/usr/bin/env bash

set -euo pipefail

# Do not edit the coverage badge number by hand. Regenerate it from cov.out
# with scripts/coverage-badge.sh --write.

if [[ $# -ne 1 || ( "$1" != "--write" && "$1" != "--check" ) ]]; then
  printf 'usage: %s <--write|--check>\n' "$0" >&2
  exit 1
fi

export LC_ALL=C

mode=$1
repo_root=$(git rev-parse --show-toplevel)
profile_path="$repo_root/cov.out"
readme_path="$repo_root/README.md"

if [[ ! -f "$profile_path" ]]; then
  printf '%s: coverage profile not found; generate cov.out first\n' "$0" >&2
  exit 1
fi

coverage=$(go tool cover -func="$profile_path" | tail -1 | awk '{print $3}')
if [[ ! "$coverage" =~ ^([0-9]+([.][0-9]+)?)%$ ]]; then
  printf '%s: invalid coverage total: %s\n' "$0" "$coverage" >&2
  exit 1
fi
measured=${BASH_REMATCH[1]}

badge_count=$(grep -Ec 'img[.]shields[.]io/badge/coverage-[0-9]+%25-' "$readme_path" || true)
if [[ "$badge_count" -ne 1 ]]; then
  printf '%s: expected exactly one static coverage badge, found %s\n' "$0" "$badge_count" >&2
  exit 1
fi

published=$(sed -nE 's#.*img[.]shields[.]io/badge/coverage-([0-9]+)%25-[^)]*.*#\1#p' "$readme_path")

if [[ "$mode" == "--write" ]]; then
  rounded=$(awk -v value="$measured" 'BEGIN { printf "%.0f", value }')
  temporary=$(mktemp "$repo_root/.README.coverage.XXXXXX")
  trap 'rm -f "$temporary"' EXIT
  sed -E "s#(img[.]shields[.]io/badge/coverage-)[0-9]+%25-#\\1${rounded}%25-#" \
    "$readme_path" > "$temporary"
  cp "$temporary" "$readme_path"
  printf 'coverage badge updated: published=%s%% measured=%s%%\n' "$rounded" "$measured"
  exit 0
fi

delta=$(awk -v published="$published" -v measured="$measured" '
  BEGIN {
    difference = published - measured
    if (difference < 0) {
      difference = -difference
    }
    printf "%.1f", difference
  }
')

if ! awk -v delta="$delta" 'BEGIN { exit !(delta <= 1.0) }'; then
  printf '%s: coverage badge drift: published=%s%% measured=%s%% delta=%s points (allowed: 1.0)\n' \
    "$0" "$published" "$measured" "$delta" >&2
  exit 1
fi

printf 'coverage badge current: published=%s%% measured=%s%% delta=%s points\n' \
  "$published" "$measured" "$delta"
