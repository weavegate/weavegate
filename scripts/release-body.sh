#!/usr/bin/env bash

set -euo pipefail

usage() {
  printf 'usage: %s verify <expected-path> <actual-path> | --self-test\n' "$0" >&2
}

normalize_body() {
  local source_path=$1
  local normalized_path=$2
  local last_byte

  cp -- "$source_path" "$normalized_path"
  while [[ -s $normalized_path ]]; do
    last_byte=$(tail -c 1 "$normalized_path" | od -An -t u1)
    last_byte=${last_byte//[[:space:]]/}
    if [[ $last_byte != 10 ]]; then
      break
    fi
    truncate -s -1 "$normalized_path"
  done
}

verify() {
  if [[ $# -ne 2 ]]; then
    usage
    return 1
  fi

  local expected_path=$1
  local actual_path=$2

  if [[ ! -f $expected_path ]]; then
    printf 'release body: expected file does not exist\n' >&2
    return 1
  fi
  if [[ ! -f $actual_path ]]; then
    printf 'release body: actual file does not exist\n' >&2
    return 1
  fi

  local verification_root
  verification_root=$(mktemp -d)
  normalize_body "$expected_path" "$verification_root/expected"
  normalize_body "$actual_path" "$verification_root/actual"

  local result=0
  if [[ ! -s $verification_root/expected ]]; then
    printf 'release body: expected body is empty\n' >&2
    result=1
  elif [[ ! -s $verification_root/actual ]]; then
    printf 'release body: actual body is empty\n' >&2
    result=1
  elif ! cmp -s "$verification_root/expected" "$verification_root/actual"; then
    printf 'release body: published body does not match expected body\n' >&2
    result=1
  fi

  rm -rf -- "$verification_root"
  return "$result"
}

self_test() {
  release_body_test_root=$(mktemp -d)
  trap 'rm -rf "$release_body_test_root"' EXIT

  printf '## Notes\n\nRelease body.\n' > "$release_body_test_root/exact-expected.md"
  printf '## Notes\n\nRelease body.\n' > "$release_body_test_root/exact-actual.md"
  verify "$release_body_test_root/exact-expected.md" "$release_body_test_root/exact-actual.md"
  printf 'EXACT_BODY_ACCEPTED\n'

  printf '## Notes\n\nRelease body.\n' > "$release_body_test_root/newlines-expected.md"
  printf '## Notes\n\nRelease body.\n\n\n' > "$release_body_test_root/newlines-actual.md"
  verify "$release_body_test_root/newlines-expected.md" "$release_body_test_root/newlines-actual.md"
  printf 'TRAILING_NEWLINES_NORMALIZED\n'

  printf '\n\n' > "$release_body_test_root/empty-expected.md"
  if verify "$release_body_test_root/empty-expected.md" "$release_body_test_root/exact-actual.md" 2> /dev/null; then
    printf 'release body self-test: accepted an empty expected body\n' >&2
    return 1
  fi
  printf 'EMPTY_EXPECTED_CAUGHT\n'

  printf '\n\n' > "$release_body_test_root/empty-actual.md"
  if verify "$release_body_test_root/exact-expected.md" "$release_body_test_root/empty-actual.md" 2> /dev/null; then
    printf 'release body self-test: accepted an empty actual body\n' >&2
    return 1
  fi
  printf 'EMPTY_ACTUAL_CAUGHT\n'

  printf '## Notes\n\nRelease  body.\n' > "$release_body_test_root/mismatch.md"
  if verify "$release_body_test_root/exact-expected.md" "$release_body_test_root/mismatch.md" 2> /dev/null; then
    printf 'release body self-test: accepted a mismatched body\n' >&2
    return 1
  fi

  printf '## Notes\n\nRelease\0 body.\n' > "$release_body_test_root/nul-mismatch.md"
  if verify "$release_body_test_root/exact-expected.md" "$release_body_test_root/nul-mismatch.md" 2> /dev/null; then
    printf 'release body self-test: accepted an internal NUL mismatch\n' >&2
    return 1
  fi
  printf 'BODY_MISMATCH_CAUGHT\n'

  printf '%s\n' 'RELEASE_BODY_RESULT exact=accepted trailing_newlines=normalized expected_empty=rejected actual_empty=rejected mismatch=rejected'
}

if [[ ${1:-} == '--self-test' ]]; then
  if [[ $# -ne 1 ]]; then
    usage
    exit 1
  fi
  self_test
  exit
fi

if [[ ${1:-} == 'verify' ]]; then
  shift
  verify "$@"
  exit
fi

usage
exit 1
