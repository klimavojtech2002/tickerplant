#!/bin/sh
# The whole stack, checked from a browser: builds and starts the edge (synthetic source, with faults injected), serves the
# built dashboard with the real nginx image (check-image.sh), and opens it in Chrome (browser-check.mjs).
#
#   cd web && pnpm build && sh scripts/check-stack.sh
#
# Needs Go, Docker and Chrome (CHROME_BIN, or a usual install path), and port 8080 free.
set -eu

cd "$(dirname "$0")/.."
[ -f out/index.html ] || { echo "run pnpm build first" >&2; exit 1; }

TMP="$(mktemp -d)"
EDGE_PID=""
cleanup() {
  [ -z "$EDGE_PID" ] || kill "$EDGE_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

(cd .. && go build -o "$TMP/edge" ./cmd/tickerplant)
"$TMP/edge" -http 127.0.0.1:8080 -steps 100000000 -chaos > "$TMP/edge.log" 2>&1 &
EDGE_PID=$!

i=0
until curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "the edge did not become ready:" >&2
    cat "$TMP/edge.log" >&2
    exit 1
  fi
  sleep 1
done

BROWSER_CHECK=1 sh scripts/check-image.sh
