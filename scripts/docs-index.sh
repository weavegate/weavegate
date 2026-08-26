#!/usr/bin/env bash

set -euo pipefail

self_test_root=''

extract_index_entries() {
  local index_file=$1

  LC_ALL=C awk '
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

    function hex_value(character) {
      character = tolower(character)
      return index("0123456789abcdef", character) - 1
    }

    function percent_decode(text, decoded, offset, high, low, byte) {
      decoded = ""
      percent_error = 0
      for (offset = 1; offset <= length(text); offset++) {
        if (substr(text, offset, 1) != "%") {
          decoded = decoded substr(text, offset, 1)
          continue
        }
        high = hex_value(substr(text, offset + 1, 1))
        low = hex_value(substr(text, offset + 2, 1))
        if (high < 0 || low < 0) {
          percent_error = 1
          return ""
        }
        byte = high * 16 + low
        if (byte < 32 || byte == 127) {
          percent_error = 2
          return ""
        }
        decoded = decoded sprintf("%c", byte)
        offset += 2
      }
      return decoded
    }

    function malformed(reason) {
      printf "MALFORMED\t%d\t%s\t%s\n", NR, reason, original
    }

    {
      original = $0

      if (fence) {
        match(original, /^ */)
        indentation = RLENGTH
        rest = substr(original, indentation + 1)
        run = fence_run(rest)
        if (indentation <= 3 && substr(rest, 1, 1) == fence_character &&
            run >= fence_length && substr(rest, run + 1) ~ /^[[:space:]]*$/) {
          fence = 0
        }
        next
      }

      line = without_comments(original)
      match(line, /^ */)
      indentation = RLENGTH
      rest = substr(line, indentation + 1)
      run = fence_run(rest)

      if (indentation <= 3 && run >= 3 &&
          (substr(rest, 1, 1) == "~" || substr(rest, run + 1) !~ /`/)) {
        fence = 1
        comment = 0
        fence_character = substr(rest, 1, 1)
        fence_length = run
        next
      }

      if (original !~ /^[[:space:]]*- \[[^]]+\]\(/ ||
          line !~ /^[[:space:]]*- \[[^]]+\]\(/) {
        next
      }
      if (indentation >= 4) {
        next
      }
      if (line !~ /^- /) {
        malformed("entry must be a top-level list item")
        next
      }

      value = line
      sub(/^- \[[^]]+\]\(/, "", value)
      if (!sub(/\)[[:space:]]+—[[:space:]]+[^[:space:]].*$/, "", value)) {
        malformed("entry requires a nonempty em-dash description")
        next
      }

      if (substr(value, 1, 1) == "<") {
        closing = index(value, ">")
        if (!closing) {
          malformed("angle-bracket destination is not closed")
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
        malformed("link title is not a supported CommonMark title")
        next
      }

      sub(/#.*/, "", destination)
      destination = percent_decode(destination)
      if (percent_error == 1) {
        malformed("destination contains an invalid percent escape")
        next
      }
      if (percent_error == 2) {
        malformed("destination contains a percent-encoded control character")
        next
      }
      sub(/^\.\//, "", destination)
      if (destination ~ /\.md$/) {
        print "PATH\tdocs/" destination
      }
    }
  ' "$index_file"
}

check_index() {
  local requested_root=$1
  local repository_root
  local check_root
  local tree_file
  local raw_index_file
  local index_file
  local duplicate_paths
  local malformed_entries
  local orphan_paths
  local ghost_paths
  local percent_paths
  local pages
  local indexed
  local -a tree_paths
  local -a index_paths

  repository_root=$(git -C "$requested_root" rev-parse --show-toplevel)
  check_root=$(mktemp -d)
  tree_file="$check_root/tree"
  raw_index_file="$check_root/index.raw"
  index_file="$check_root/index"

  if ! git -C "$repository_root" ls-files --error-unmatch -- docs/README.md \
    > /dev/null 2>&1; then
    echo 'documentation index is not tracked: docs/README.md' >&2
    rm -rf -- "$check_root"
    return 1
  fi
  if [[ ! -r "$repository_root/docs/README.md" ]]; then
    echo 'tracked documentation index is not readable: docs/README.md' >&2
    rm -rf -- "$check_root"
    return 1
  fi

  if ! git -C "$repository_root" -c core.quotePath=false ls-files docs/ \
    | awk '/\.md$/ && $0 != "docs/README.md"' \
    | sort > "$tree_file"; then
    echo 'failed to read the tracked documentation inventory' >&2
    rm -rf -- "$check_root"
    return 1
  fi
  mapfile -t tree_paths < "$tree_file"
  if [[ ${#tree_paths[@]} -eq 0 ]]; then
    echo 'documentation inventory has no tracked Markdown pages under docs/' >&2
    rm -rf -- "$check_root"
    return 1
  fi

  percent_paths=$(awk 'index($0, "%")' "$tree_file")
  if [[ -n "$percent_paths" ]]; then
    printf 'tracked documentation paths containing %% are unsupported because percent-encoded destinations would be ambiguous:\n%s\n' \
      "$percent_paths" >&2
    rm -rf -- "$check_root"
    return 1
  fi

  if ! extract_index_entries "$repository_root/docs/README.md" > "$raw_index_file"; then
    echo 'failed to extract documentation index entries' >&2
    rm -rf -- "$check_root"
    return 1
  fi
  malformed_entries=$(awk -F '\t' '$1 == "MALFORMED" {
    printf "line %s: %s: %s\n", $2, $3, substr($0, index($0, $4))
  }' "$raw_index_file")
  if [[ -n "$malformed_entries" ]]; then
    printf 'malformed documentation index entries:\n%s\n' "$malformed_entries" >&2
    rm -rf -- "$check_root"
    return 1
  fi
  if ! awk -F '\t' '$1 == "PATH" { print substr($0, 6) }' "$raw_index_file" \
    | sort > "$index_file"; then
    echo 'failed to normalize documentation index entries' >&2
    rm -rf -- "$check_root"
    return 1
  fi
  mapfile -t index_paths < "$index_file"

  duplicate_paths=$(uniq -d "$index_file")
  if [[ -n "$duplicate_paths" ]]; then
    printf 'duplicate documentation index entries:\n%s\n' "$duplicate_paths" >&2
    rm -rf -- "$check_root"
    return 1
  fi

  orphan_paths=$(comm -23 "$tree_file" "$index_file")
  ghost_paths=$(comm -13 "$tree_file" "$index_file")

  if [[ -n "$orphan_paths" ]]; then
    printf '%s\n%s\n' \
      'tracked documentation missing from top-level index entries (expected `- [title](path.md) — description`; reference-style and mid-line links are not index entries):' \
      "$orphan_paths" >&2
  fi
  if [[ -n "$ghost_paths" ]]; then
    printf 'documentation index entries missing from tree:\n%s\n' "$ghost_paths" >&2
  fi
  if [[ -n "$orphan_paths" || -n "$ghost_paths" ]]; then
    rm -rf -- "$check_root"
    return 1
  fi

  pages=${#tree_paths[@]}
  indexed=${#index_paths[@]}
  printf 'DOCS_INDEX_RESULT pages=%d indexed=%d orphans=0 ghosts=0 duplicates=0\n' \
    "$pages" "$indexed"
  rm -rf -- "$check_root"
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

  printf '# index\n\n- [a](a.md) — a\n- [ghost](ghost.md) — ghost\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if check_index "$self_test_root" > /dev/null 2>&1; then
    echo 'docs index guard accepted an entry missing from the tree' >&2
    return 1
  fi
  echo 'GHOST_ENTRY_CAUGHT'

  printf '# index\n\n- [a](a.md) — a\n- [again](a.md) — duplicate\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if check_index "$self_test_root" > /dev/null 2>&1; then
    echo 'docs index guard accepted a duplicate entry' >&2
    return 1
  fi
  echo 'DUPLICATE_ENTRY_CAUGHT'

  printf '# index\n\n- [a](a.md) — a\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  output_one=$(check_index "$self_test_root")
  [[ "$output_one" == 'DOCS_INDEX_RESULT pages=1 indexed=1 orphans=0 ghosts=0 duplicates=0' ]]

  printf '# b\n' > "$self_test_root/docs/b.md"
  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md docs/b.md
  output_two=$(check_index "$self_test_root")
  [[ "$output_two" == 'DOCS_INDEX_RESULT pages=2 indexed=2 orphans=0 ghosts=0 duplicates=0' ]]
  echo 'MARKER_TRACKS_COUNT'

  printf 'asset\n' > "$self_test_root/docs/image.png"
  git -C "$self_test_root" add docs/image.png
  check_index "$self_test_root" > /dev/null
  echo 'ASSET_IGNORED'

  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n\n```text\n- [example](missing.md)\n```\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'FENCED_EXAMPLE_IGNORED'

  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n\n~~~text\n- [example](missing.md)\n~~~\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'TILDE_FENCE_IGNORED'

  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n\n````text\n```text\n- [example](missing.md)\n```\n````\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'LONG_FENCE_IGNORED'

  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n\n```text\n    ```\n- [example](missing.md) — example\n```\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'INDENTED_CLOSER_IGNORED'

  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n\nFormat: `- [example](missing.md)`\n\n[example][ref]\n[ref]: missing.md\n\n<!-- note --> - [example](missing.md)\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'INLINE_SPAN_IGNORED'

  printf '# index\n\n- [a](a.md "title") — a\n- [b](b.md) — b\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  printf "# index\n\n- [a](a.md 'title') — a\n- [b](b.md) — b\n" \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  printf '# index\n\n- [a](a.md (title)) — a\n- [b](b.md) — b\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'TITLED_LINK_INDEXED'

  printf '# index\n\n- [a](./a.md#section) — a\n- [b](b.md) — b\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'DOTSLASH_NORMALIZED'

  printf '# index\n\n- [a](<a.md>) — a\n- [b](b.md) — b\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'ANGLE_DEST_INDEXED'

  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n- [ghost](ghost.md) — ghost\n' \
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

  rm -f -- "$self_test_root/docs/README.md"
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted a missing tracked index file' >&2
    return 1
  fi
  [[ "$output_one" == *'tracked documentation index is not readable: docs/README.md'* ]]
  echo 'INDEX_MISSING_CAUGHT'
  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md

  git -C "$self_test_root" rm --cached -q docs/README.md
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted an untracked working-tree index' >&2
    return 1
  fi
  [[ "$output_one" == *'documentation index is not tracked: docs/README.md'* ]]
  echo 'INDEX_UNTRACKED_CAUGHT'
  git -C "$self_test_root" add docs/README.md

  git -C "$self_test_root" rm --cached -q docs/a.md docs/b.md
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted an empty tracked page inventory' >&2
    return 1
  fi
  [[ "$output_one" == *'documentation inventory has no tracked Markdown pages under docs/'* ]]
  echo 'EMPTY_TREE_CAUGHT'
  git -C "$self_test_root" add docs/a.md docs/b.md

  printf '# index\n\n```text\n<!-- unmatched example comment\n```\n\n- [a](a.md) — a\n- [b](b.md) — b\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'FENCED_COMMENT_IGNORED'

  printf '# index\n\n<!-- draft\n```\n-->\n\n- [a](a.md) — a\n- [b](b.md) — b\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'COMMENTED_FENCE_IGNORED'

  printf '# index\n\n- [a](a.md)\n- [b](b.md) — b\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted an entry without a description' >&2
    return 1
  fi
  [[ "$output_one" == *'malformed documentation index entries:'* ]]
  [[ "$output_one" == *'entry requires a nonempty em-dash description'* ]]
  printf '# index\n\n- [a](a.md) —   \n- [b](b.md) — b\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted an empty description' >&2
    return 1
  fi
  [[ "$output_one" == *'entry requires a nonempty em-dash description'* ]]
  echo 'MISSING_DESCRIPTION_CAUGHT'

  printf '# index\n\n- [a](a.md) - a\n  - [b](b.md) — b\n' > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted malformed documentation entries' >&2
    return 1
  fi
  [[ "$output_one" == *'malformed documentation index entries:'* ]]
  [[ "$output_one" == *'entry requires a nonempty em-dash description'* ]]
  [[ "$output_one" == *'entry must be a top-level list item'* ]]
  [[ "$output_one" != *'tracked documentation missing from top-level index entries'* ]]
  echo 'MALFORMED_ENTRY_DISTINCT'

  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n\n    - [example](missing.md) — example\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md
  check_index "$self_test_root" > /dev/null
  echo 'INDENTED_BLOCK_IGNORED'

  printf '# c\n' > "$self_test_root/docs/c.md"
  printf '# index\n\n- [a](a.md) — a\n- [both](b.md%%0APATH%%09docs/c.md) — b\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md docs/c.md
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted a percent-decoded record injection' >&2
    return 1
  fi
  [[ "$output_one" == *'destination contains a percent-encoded control character'* ]]
  echo 'CONTROL_ESCAPE_CAUGHT'
  git -C "$self_test_root" rm -q -f docs/c.md

  printf '# c-sharp guide\n' > "$self_test_root/docs/c#-guide.md"
  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n- [c](c%%23-guide.md) — c\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md 'docs/c#-guide.md'
  check_index "$self_test_root" > /dev/null
  echo 'PERCENT_DEST_NORMALIZED'

  printf '# café guide\n' > "$self_test_root/docs/café.md"
  printf '# index\n\n- [a](a.md) — a\n- [b](b.md) — b\n- [c](c%%23-guide.md) — c\n- [café](caf%%C3%%A9.md) — café\n' \
    > "$self_test_root/docs/README.md"
  git -C "$self_test_root" add docs/README.md 'docs/café.md'
  check_index "$self_test_root" > /dev/null
  echo 'UTF8_DEST_NORMALIZED'

  printf '# ambiguous percent path\n' > "$self_test_root/docs/c%23-guide.md"
  git -C "$self_test_root" add 'docs/c%23-guide.md'
  if output_one=$(check_index "$self_test_root" 2>&1); then
    echo 'docs index guard accepted an ambiguous percent-bearing tracked path' >&2
    return 1
  fi
  [[ "$output_one" == *'percent-encoded destinations would be ambiguous'* ]]
  echo 'PERCENT_FILENAME_CAUGHT'
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
