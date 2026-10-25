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
trap 'exit 130' INT
trap 'exit 143' TERM

# A leftover process on the port would answer for the edge this script is about to start.
if curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
  echo "something already answers on 127.0.0.1:8080; stop it first" >&2
  exit 1
fi

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
  if ! kill -0 "$EDGE_PID" 2>/dev/null; then
    echo "the edge exited:" >&2
    cat "$TMP/edge.log" >&2
    exit 1
  fi
  sleep 1
done

if ! BROWSER_CHECK=1 REQUIRE_BOOK=1 REQUIRE_HERO=1 REQUIRE_MARKET=1 REQUIRE_INTEGRITY=1 sh scripts/check-image.sh; then
  echo "--- edge log ---" >&2
  tail -n 40 "$TMP/edge.log" >&2
  exit 1
fi

# The long watches in the browser check are meant to span an injected fault; make sure one happened.
if ! curl -fsS http://127.0.0.1:8080/api/status | grep -Eq '"resyncs":[1-9]'; then
  echo "no fault was injected during the run, so the layout was not watched across a repair" >&2
  exit 1
fi
