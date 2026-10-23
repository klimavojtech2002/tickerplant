#!/bin/sh
# Versions that are written twice must agree. Run from the repository root.
#
#   go.mod toolchain          == the golang image tag in Dockerfile
#   web/package.json pnpm     == the pnpm version in web/Dockerfile (corepack prepare)
#   web/package.json node     == the node image major in web/Dockerfile
# (The Go version the runner uses comes from go.mod itself, and ci.yml compares it with the toolchain line.)
set -eu

root="${1:-.}"
status=0
fail() { echo "version drift: $*"; status=1; }

toolchain="$(awk '$1 == "toolchain" { sub(/^go/, "", $2); print $2 }' "$root/go.mod")"
tags="$(sed -n 's/^FROM golang:\([0-9][0-9.]*\)-alpine.*/\1/p' "$root/Dockerfile")"
if [ -z "$toolchain" ]; then fail "go.mod has no toolchain line"; fi
if [ -z "$tags" ]; then fail "Dockerfile has no golang:<version>-alpine image"; fi
for tag in $tags; do
  [ "$tag" = "$toolchain" ] || fail "go.mod toolchain is go$toolchain but Dockerfile uses golang:$tag"
done

pm="$(sed -n 's/.*"packageManager": *"pnpm@\([0-9][0-9.]*\)".*/\1/p' "$root/web/package.json")"
corepack="$(sed -n 's/.*corepack prepare pnpm@\([0-9][0-9.]*\).*/\1/p' "$root/web/Dockerfile")"
if [ -z "$pm" ] || [ -z "$corepack" ]; then fail "could not read the pnpm version from web/package.json or web/Dockerfile"
elif [ "$pm" != "$corepack" ]; then fail "packageManager is pnpm@$pm but web/Dockerfile prepares pnpm@$corepack"; fi

engines="$(sed -n 's/.*"node": *">=\([0-9][0-9]*\)".*/\1/p' "$root/web/package.json")"
image="$(sed -n 's/^FROM node:\([0-9][0-9]*\)-alpine.*/\1/p' "$root/web/Dockerfile")"
if [ -z "$engines" ] || [ -z "$image" ]; then fail "could not read the node version from web/package.json or web/Dockerfile"
elif [ "$engines" != "$image" ]; then fail "engines.node is >=$engines but web/Dockerfile uses node:$image"; fi

[ "$status" -eq 0 ] && echo "versions agree: go$toolchain, pnpm $pm, node $image"
exit "$status"
