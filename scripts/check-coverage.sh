#!/bin/sh
# Checks statement coverage per package against a committed minimum.
#
#   sh scripts/check-coverage.sh coverage.out packages.txt .github/coverage-min.txt <module path>
#
# coverage.out  a Go cover profile (go test -coverprofile)
# packages.txt  the import path of every package, one per line (go list ./...)
# coverage-min  one line per package: "<path relative to the module> <minimum percent>" to gate it,
#               or "<path> exclude <reason>" to leave it out. "#" starts a comment.
#
# Every package must be on one list or the other, so a new package has to choose. A gated package
# that is absent from the profile (no statements, or it failed to build) fails. The percentage is
# covered statements over all statements, from the profile, not from the rounded test output.
set -eu

[ "$#" -eq 4 ] || { echo "usage: check-coverage.sh <coverage.out> <packages.txt> <coverage-min> <module>" >&2; exit 2; }
profile="$1"; packages="$2"; minimums="$3"; module="$4"

# The names go in through the environment: awk -v would read backslashes in a Windows path as escapes.
MODULE="$module" MINFILE="$minimums" PKGFILE="$packages" awk '
BEGIN {
  module = ENVIRON["MODULE"]; minfile = ENVIRON["MINFILE"]; pkgfile = ENVIRON["PKGFILE"]
  while ((getline line < minfile) > 0) {
    sub(/#.*/, "", line)
    n = split(line, f, /[ \t]+/)
    if (n == 0 || f[1] == "") continue
    if (f[2] == "exclude") { excluded[f[1]] = 1; continue }
    if (f[2] == "" || f[2] !~ /^[0-9]+(\.[0-9]+)?$/) { printf "bad line in %s: %s\n", minfile, line; bad = 1; continue }
    min[f[1]] = f[2] + 0
  }
  while ((getline line < pkgfile) > 0) {
    if (line == "") continue
    rel = line
    if (index(rel, module "/") == 1) rel = substr(rel, length(module) + 2)
    else if (rel == module) rel = "."
    listed[rel] = 1
  }
}
NR == 1 && /^mode:/ { next }
{
  # path/file.go:l.c,l.c statements count
  colon = index($0, ":")
  file = substr($0, 1, colon - 1)
  rest = substr($0, colon + 1)
  split(rest, p, " ")
  stmts = p[2] + 0; cnt = p[3] + 0
  dir = file; sub(/\/[^\/]*$/, "", dir)
  if (index(dir, module "/") == 1) dir = substr(dir, length(module) + 2)
  else if (dir == module) dir = "."
  total[dir] += stmts
  if (cnt > 0) covered[dir] += stmts
}
END {
  status = bad
  for (pkg in listed) {
    if (pkg in excluded) continue
    if (!(pkg in min)) { printf "FAIL %s is on neither list in %s\n", pkg, minfile; status = 1; continue }
    if (!(pkg in total) || total[pkg] == 0) { printf "FAIL %s has no statements in the profile\n", pkg; status = 1; continue }
    pct = 100 * covered[pkg] / total[pkg]
    if (pct + 0.0001 < min[pkg]) { printf "FAIL %s %.1f%% is below the minimum %.1f%%\n", pkg, pct, min[pkg]; status = 1 }
    else printf "ok   %s %.1f%% (minimum %.1f%%)\n", pkg, pct, min[pkg]
  }
  for (pkg in min) if (!(pkg in listed)) { printf "FAIL %s is in %s but is not a package\n", pkg, minfile; status = 1 }
  for (pkg in excluded) if (!(pkg in listed)) { printf "FAIL %s is excluded in %s but is not a package\n", pkg, minfile; status = 1 }
  exit status
}' "$profile"
