#!/usr/bin/env bash

set -euo pipefail

self_test_root=''

check_index() {
  local requested_root=$1
  local repository_root
  local duplicate_paths
  local orphan_paths
  local ghost_paths
  local pages
  local indexed

  repository_root=$(git -C "$requested_root" rev-parse --show-toplevel)

  mapfile -t tree_paths < <(
    git -C "$repository_root" -c core.quotePath=false ls-files docs/ \
      | grep -E '\.md$' \
      | grep -v '^docs/README\.md$' \
      | sort
  )
  mapfile -t index_paths < <(
    awk '
      function fence_run(text, character, run_length) {
        character = substr(text, 1, 1)
        if (character != "`" && character != "~") {
          return 0
        }
        for (run_length = 1; substr(text, run_length, 1) == character; run_length++) {
        }
        return run_length - 1
      }

      function without_comments(text, visible, opening, closing) {
        visible = ""
        while (1) {
          if (comment) {
            closing = index(text, "-->")
            if (!closing) {
              return visible
            }
            text = substr(text, closing + 3)
            visible = visible " "
            comment = 0
          }

          opening = index(text, "<!--")
          if (!opening) {
            return visible text
          }
          visible = visible substr(text, 1, opening - 1) " "
          text = substr(text, opening + 4)
          comment = 1
        }
      }

      {
        line = without_comments($0)
        match(line, /^ */)
        indentation = RLENGTH
        rest = substr(line, indentation + 1)
        run = fence_run(rest)

        if (fence) {
          if (substr(rest, 1, 1) == fence_character &&
              run >= fence_length && substr(rest, run + 1) ~ /^[[:space:]]*$/) {
            fence = 0
          }
          next
        }

        if (indentation <= 3 && run >= 3 &&
            (substr(rest, 1, 1) == "~" || substr(rest, run + 1) !~ /`/)) {
          fence = 1
          fence_character = substr(rest, 1, 1)
          fence_length = run
          next
        }

        print line
      }
    ' "$repository_root/docs/README.md" \
      | grep -E '^- \[[^]]+\]\(' \
      | awk '
        {
          value = $0
          sub(/^- \[[^]]+\]\(/, "", value)
          if (!sub(/\)[[:space:]]*(—.*)?$/, "", value)) {
            next
          }

          if (substr(value, 1, 1) == "<") {
            closing = index(value, ">")
            if (!closing) {
              next
            }
            destination = substr(value, 2, closing - 2)
            title = substr(value, closing + 1)
          } else {
            match(value, /[[:space:]]/)
            if (RSTART) {
              destination = substr(value, 1, RSTART - 1)
              title = substr(value, RSTART + 1)
            } else {
              destination = value
              title = ""
            }
          }

          sub(/^[[:space:]]+/, "", title)
          sub(/[[:space:]]+$/, "", title)
          if (title != "" && title !~ /^"[^"]*"$/ &&
              title !~ /^\047[^\047]*\047$/ && title !~ /^\([^()]*\)$/) {
            next
          }

          sub(/#.*/, "", destination)
          sub(/^\.\//, "", destination)
          if (destination ~ /\.md$/) {
            print "docs/" destination
          }
        }
      ' \
      | sort
  )

  duplicate_paths=$(printf '%s\n' "${index_paths[@]}" | uniq -d)
  if [[ -n "$duplicate_paths" ]]; then
    printf 'duplicate documentation index entries:\n%s\n' "$duplicate_paths" >&2
    return 1
  fi

  orphan_paths=$(comm -23 \
    <(printf '%s\n' "${tree_paths[@]}") \
    <(printf '%s\n' "${index_paths[@]}"))
  ghost_paths=$(comm -13 \
    <(printf '%s\n' "${tree_paths[@]}") \
    <(printf '%s\n' "${index_paths[@]}"))

  if [[ -n "$orphan_paths" ]]; then
    printf '%s\n%s\n' \
      'tracked documentation missing from top-level index entries (expected `- [title](path.md) — description`; reference-style and mid-line links are not index entries):' \
      "$orphan_paths" >&2
  fi
  if [[ -n "$ghost_paths" ]]; then
    printf 'documentation index entries missing from tree:\n%s\n' "$ghost_paths" >&2
  fi
  if [[ -n "$orphan_paths" || -n "$ghost_paths" ]]; then
    return 1
  fi

  pages=${#tree_paths[@]}
  indexed=${#index_paths[@]}
  printf 'DOCS_INDEX_RESULT pages=%d indexed=%d orphans=0 ghosts=0 duplicates=0\n' \
    "$pages" "$indexed"
}

run_self_test() {
  local output_one
  local output_two

  self_test_root=$(mktemp -d)
  trap 'rm -rf -- "${self_test_root:?}"' EXIT
  git -C "$self_test_root" init -q
  mkdir -p "$self_test_root/docs"

  printf '# index\n' > "$self_test_root/docs/README.md"
  printf '# a\n' > "$self_test_root/docs/a.md"
  git -C "$self_test_root" add docs/README.md docs/a.md
  if check_index "$self_test_root" > /dev/null 2>&1; then
    echo 'docs index guard accepted a page missing from the index' >&2
    return 1
  fi
  echo 'MISSING_FROM_INDEX_CAUGHT'

  printf '# index\n\n- [a](a.md)\n- [ghost](ghost.md)\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if check_index "$self_test_root" > /dev/null 2>&1; then
    echo 'docs index guard accepted an entry missing from the tree' >&2
    return 1
  fi
  echo 'GHOST_ENTRY_CAUGHT'

  printf '# index\n\n- [a](a.md)\n- [again](a.md)\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if check_index "$self_test_root" > /dev/null 2>&1; then
    echo 'docs index guard accepted a duplicate entry' >&2
    return 1
  fi
  echo 'DUPLICATE_ENTRY_CAUGHT'

  printf '# index\n\n- [a](a.md)\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  output_one=$(check_index "$self_test_root")
  [[ "$output_one" == 'DOCS_INDEX_RESULT pages=1 indexed=1 orphans=0 ghosts=0 duplicates=0' ]]

  printf '# b\n' > "$self_test_root/docs/b.md"
  printf '# index\n\n- [a](a.md)\n- [b](b.md)\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md docs/b.md
  output_two=$(check_index "$self_test_root")
  [[ "$output_two" == 'DOCS_INDEX_RESULT pages=2 indexed=2 orphans=0 ghosts=0 duplicates=0' ]]
  echo 'MARKER_TRACKS_COUNT'

  printf 'asset\n' > "$self_test_root/docs/image.png"
  git -C "$self_test_root" add docs/image.png
  check_index "$self_test_root" > /dev/null
  echo 'ASSET_IGNORED'

  printf '# index\n\n- [a](a.md)\n- [b](b.md)\n\n```text\n- [example](missing.md)\n```\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'FENCED_EXAMPLE_IGNORED'

  printf '# index\n\n- [a](a.md)\n- [b](b.md)\n\n~~~text\n- [example](missing.md)\n~~~\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'TILDE_FENCE_IGNORED'

  printf '# index\n\n- [a](a.md)\n- [b](b.md)\n\n````text\n```text\n- [example](missing.md)\n```\n````\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'LONG_FENCE_IGNORED'

  printf '# index\n\n- [a](a.md)\n- [b](b.md)\n\nFormat: `- [example](missing.md)`\n\n[example][ref]\n[ref]: missing.md\n\n<!-- note --> - [example](missing.md)\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'INLINE_SPAN_IGNORED'

  printf '# index\n\n- [a](a.md "title")\n- [b](b.md)\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  printf "# index\n\n- [a](a.md 'title')\n- [b](b.md)\n" \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  printf '# index\n\n- [a](a.md (title))\n- [b](b.md)\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'TITLED_LINK_INDEXED'

  printf '# index\n\n- [a](./a.md#section)\n- [b](b.md)\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'DOTSLASH_NORMALIZED'

  printf '# index\n\n- [a](<a.md>)\n- [b](b.md)\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'ANGLE_DEST_INDEXED'

  printf '# index\n\n- [a](a.md)\n- [b](b.md)\n- [ghost](ghost.md)\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if check_index "$self_test_root" > /dev/null 2>&1; then
    echo 'docs index guard stopped rejecting an ordinary ghost entry' >&2
    return 1
  fi
  echo 'GHOST_STILL_BITES'

  printf '# index\n\n```text\n- [a](a.md)\n- [b](b.md)\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if check_index "$self_test_root" > /dev/null 2>&1; then
    echo 'docs index guard accepted pages hidden by an unclosed fence' >&2
    return 1
  fi
  echo 'UNCLOSED_FENCE_STILL_BITES'
}

if [[ ${1:-} == '--self-test' ]]; then
  if [[ $# -ne 1 ]]; then
    printf 'usage: %s --self-test | [root]\n' "$0" >&2
    exit 1
  fi
  run_self_test
  exit 0
fi

if [[ $# -gt 1 ]]; then
  printf 'usage: %s --self-test | [root]\n' "$0" >&2
  exit 1
fi

check_index "${1:-.}"
