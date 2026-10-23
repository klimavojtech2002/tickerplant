#!/bin/sh
# Tests for check-pins.sh and check-versions.sh on small fixtures.
#
#   sh scripts/check-pins_test.sh
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failures=0
sha=0123456789abcdef0123456789abcdef01234567

check() { # <pass|fail> <name> <exit code of the script under test>
  if [ "$3" -eq 0 ]; then got=pass; else got=fail; fi
  if [ "$got" = "$1" ]; then echo "ok   $2"; else echo "FAIL $2: wanted $1, got $got"; failures=$((failures + 1)); fi
}
pins() { # <name> <pass|fail> <line>
  printf 'jobs:\n  a:\n    steps:\n%s\n' "$3" > "$tmp/w.yml"
  rc=0; sh "$here/check-pins.sh" "$tmp/w.yml" > /dev/null || rc=$?
  check "$2" "pins: $1" "$rc"
}

pins "pinned with a version"       pass "      - uses: actions/checkout@$sha # v4.4.0"
pins "pinned, two-part version"    pass "      - uses: actions/setup-go@$sha # v6.5"
pins "a subpath action"            pass "      - uses: github/codeql-action/init@$sha # v3.1.0"
pins "a local action"              pass "      - uses: ./.github/actions/x"
pins "a major-only version comment" pass "      - uses: actions/checkout@$sha # v4"
pins "a tag"                       fail "      - uses: actions/checkout@v4"
pins "a branch"                    fail "      - uses: actions/checkout@main"
pins "a short sha"                 fail "      - uses: actions/checkout@0123456 # v4.4.0"
pins "a sha without a comment"     fail "      - uses: actions/checkout@$sha"
pins "a sha with a bad comment"    fail "      - uses: actions/checkout@$sha # latest"
pins "a docker action"             fail "      - uses: docker://alpine:3"
pins "a commented-out line only"   pass "      # uses: actions/checkout@v4"

# versions
mkversions() { # <toolchain> <image tag> <packageManager pnpm> <corepack pnpm> <engines node> <node image>
  rm -rf "$tmp/r"; mkdir -p "$tmp/r/web"
  printf 'module x\n\ngo 1.25\n\ntoolchain go%s\n' "$1" > "$tmp/r/go.mod"
  printf 'FROM golang:%s-alpine@sha256:abc AS builder\nFROM scratch\n' "$2" > "$tmp/r/Dockerfile"
  printf '{\n  "engines": {"node": ">=%s"},\n  "packageManager": "pnpm@%s"\n}\n' "$5" "$3" > "$tmp/r/web/package.json"
  printf 'FROM node:%s-alpine AS builder\nRUN corepack enable && corepack prepare pnpm@%s --activate\n' "$6" "$4" > "$tmp/r/web/Dockerfile"
}
versions() { # <name> <pass|fail>
  rc=0; sh "$here/check-versions.sh" "$tmp/r" > /dev/null || rc=$?
  check "$2" "versions: $1" "$rc"
}
mkversions 1.26.10 1.26.10 10.21.0 10.21.0 22 22; versions "all agree" pass
mkversions 1.26.10 1.26.9 10.21.0 10.21.0 22 22;  versions "image behind the toolchain" fail
mkversions 1.26.10 1.26 10.21.0 10.21.0 22 22;    versions "image without a patch" fail
mkversions 1.26.10 1.26.10 10.21.0 10.20.0 22 22; versions "pnpm differs" fail
mkversions 1.26.10 1.26.10 10.21.0 10.21.0 22 22
mkdir -p "$tmp/r/bench"; printf 'FROM golang:1.26.9-alpine AS b
' > "$tmp/r/bench/Dockerfile"; versions "bench image behind the toolchain" fail
printf 'FROM golang:1.26.10-alpine AS b
' > "$tmp/r/bench/Dockerfile"; versions "bench image agrees" pass
mkversions 1.26.10 1.26.10 10.21.0 10.21.0 22 24; versions "node differs" fail
rm "$tmp/r/Dockerfile"; printf 'FROM scratch\n' > "$tmp/r/Dockerfile"; versions "no golang image" fail

[ "$failures" -eq 0 ] || { echo "$failures case(s) failed"; exit 1; }
echo "all cases passed"
