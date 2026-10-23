#!/bin/sh
# Runs the dashboard the way the image does, without building it: the exported pages and the rendered
# headers go into the pinned nginx image, as the unprivileged user, on a read-only file system with only
# /tmp writable. Then scripts/check-served.mjs checks what comes out. Needs `pnpm build` first. `docker run`
# pulls the pinned nginx image if the machine does not have it.
#
#   sh scripts/check-image.sh            # edge URL http://127.0.0.1:8080
#   NEXT_PUBLIC_EDGE_URL=https://edge.example.com sh scripts/check-image.sh
set -eu

cd "$(dirname "$0")/.."
EDGE="${NEXT_PUBLIC_EDGE_URL:-http://127.0.0.1:8080}"
PORT="${CHECK_PORT:-18080}"
NAME="dashboard-check-$$"
IMAGE="$(sed -n 's/^FROM \(nginx:[^ ]*\) AS final/\1/p' Dockerfile)"
[ -n "$IMAGE" ] || { echo "no final nginx image in Dockerfile" >&2; exit 1; }
[ -f out/index.html ] || { echo "run pnpm build first" >&2; exit 1; }

TMP="$(mktemp -d)"
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$TMP"; }
trap cleanup EXIT

NEXT_PUBLIC_EDGE_URL="$EDGE" node scripts/csp-hash.mjs out security-headers.conf.template > "$TMP/security-headers.conf"

# Bind mounts, because a read-only container cannot be filled with `docker cp`. On Windows the paths must be
# in the form Docker Desktop expects (and MSYS must not rewrite them).
host() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else echo "$1"; fi; }
export MSYS_NO_PATHCONV=1
docker run -d --name "$NAME" --user nginx --read-only --tmpfs /tmp --cap-drop ALL   --security-opt no-new-privileges -p "127.0.0.1:$PORT:8080"   -v "$(host "$PWD/nginx.conf"):/etc/nginx/nginx.conf:ro"   -v "$(host "$TMP/security-headers.conf"):/etc/nginx/security-headers.conf:ro"   -v "$(host "$PWD/out"):/usr/share/nginx/html:ro"   "$IMAGE" >/dev/null

i=0
until curl -fsS "http://127.0.0.1:$PORT/" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "nginx did not come up:" >&2
    docker logs "$NAME" >&2
    exit 1
  fi
  sleep 1
done

# nothing at emerg, alert or crit level in the log
if docker logs "$NAME" 2>&1 | grep -E '\[(emerg|alert|crit)\]'; then
  echo "nginx logged a serious error" >&2
  exit 1
fi

node scripts/check-served.mjs "http://127.0.0.1:$PORT" "$EDGE"
