#!/bin/sh
# Tests for check-coverage.sh on small fixtures: each case says whether the check must pass or fail.
#
#   sh scripts/check-coverage_test.sh
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mod=example.com/m

# a: 3 of 4 statements covered (75%). b: 2 of 2 (100%).
cat > "$tmp/profile" <<EOP
mode: atomic
$mod/a/x.go:1.1,2.2 2 1
$mod/a/x.go:3.1,4.2 1 1
$mod/a/x.go:5.1,6.2 1 0
$mod/b/y.go:1.1,2.2 2 5
EOP
printf '%s\n' "$mod/a" "$mod/b" > "$tmp/pkgs"

failures=0
# expect <pass|fail> <name> <minimums text> [packages file]
expect() {
  want="$1"; name="$2"; printf '%s\n' "$3" > "$tmp/min"; pk="${4:-$tmp/pkgs}"
  if sh "$here/check-coverage.sh" "$tmp/profile" "$pk" "$tmp/min" "$mod" > "$tmp/out" 2>&1; then got=pass; else got=fail; fi
  if [ "$got" != "$want" ]; then
    echo "FAIL $name: wanted $want, got $got"; cat "$tmp/out"; failures=$((failures + 1))
  else
    echo "ok   $name"
  fi
}

expect pass "equal to the minimum"            "a 75
b 100"
expect pass "above the minimum"               "a 50.5
b 99"
expect fail "below the minimum"               "a 75.1
b 100"
expect fail "a package on neither list"       "a 70"
expect pass "an excluded package"             "a 70
b exclude no tests"
expect fail "a minimum for a missing package" "a 70
b 100
c 10"
expect fail "an exclusion for a missing package" "a 70
b 100
c exclude gone"
expect fail "a bad minimum"                   "a high
b 100"
expect fail "an exclusion without a reason"  "a 70
b exclude"
expect pass "indented lines"                  "   a 70
	b 100"
expect pass "comments and blank lines"        "# note

a 70 # trailing
b 100"

# A gated package with no statements in the profile.
printf '%s\n' "$mod/a" "$mod/b" "$mod/c" > "$tmp/pkgs3"
expect fail "a gated package absent from the profile" "a 70
b 100
c 10" "$tmp/pkgs3"
expect pass "an excluded package absent from the profile" "a 70
b 100
c exclude no statements" "$tmp/pkgs3"

[ "$failures" -eq 0 ] || { echo "$failures case(s) failed"; exit 1; }
echo "all cases passed"
