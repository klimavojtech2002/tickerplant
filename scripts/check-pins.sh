#!/bin/sh
# Every external action in the workflows must be pinned to a commit: "uses: owner/name@<40 hex> # vX.Y.Z".
# A tag can be moved to other code; a commit cannot. The version comment is what Dependabot keeps up to date.
#
#   sh scripts/check-pins.sh .github/workflows/*.yml
set -eu

[ "$#" -ge 1 ] || { echo "usage: check-pins.sh <workflow file>..." >&2; exit 2; }
status=0
for file in "$@"; do
  # a "uses:" line that is not a comment, and not a local action ("./...")
  grep -nE '^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]' "$file" | while IFS= read -r hit; do
    line="${hit#*:}"
    case "$line" in
      *"uses: ./"*) continue ;;
    esac
    if ! printf '%s\n' "$line" | grep -qE 'uses:[[:space:]]+[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}[[:space:]]+#[[:space:]]*v[0-9]+(\.[0-9]+){0,2}[[:space:]]*$'; then
      echo "$file:${hit%%:*}: not pinned to a commit with a version comment: $(printf '%s' "$line" | sed 's/^[[:space:]]*//')"
      exit 1
    fi
  done || status=1
done
exit "$status"
