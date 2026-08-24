#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 2 ]]; then
  printf 'usage: %s <changelog-path> <version>\n' "$0" >&2
  exit 1
fi

changelog_path=$1
version=$2

awk -v changelog_path="$changelog_path" -v version="$version" '
  BEGIN {
    heading_prefix = "## [" version "]"
  }

  index($0, heading_prefix) == 1 {
    found = 1
    heading = $0
    suffix = substr(heading, length(heading_prefix) + 1)

    if (suffix !~ /^ - [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/) {
      printf "%s: release heading has no tag date: %s; replace YYYY-MM-DD with the actual tag date\n", changelog_path, heading > "/dev/stderr"
      invalid = 1
      exit 1
    }

    capture = 1
    next
  }

  capture && index($0, "## [") == 1 { exit }
  capture { print }

  END {
    if (!found && !invalid) {
      printf "%s: has no heading for version %s\n", changelog_path, version > "/dev/stderr"
      exit 1
    }
  }
' "$changelog_path"
