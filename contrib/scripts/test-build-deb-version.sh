#!/usr/bin/env bash
# Checks how the fork version "<upstream base>+zelcore.<N>" flows into the binary VERSION.
# Needs only bash, sed and git; runs the sed expressions extracted from build-deb.sh and
# deploy-dev.sh, so the test cannot drift from the scripts. If dpkg is installed it also
# checks Debian ordering.
#
#   contrib/scripts/test-build-deb-version.sh
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
passed=0
failed=0
ok() { passed=$((passed + 1)); echo "ok   - $1"; }
bad() { failed=$((failed + 1)); echo "FAIL - $1" >&2; }
eq() { # eq NAME EXPECTED ACTUAL
    if [[ $2 == "$3" ]]; then ok "$1"; else bad "$1: expected '$2', got '$3'"; fi
}

# The sed script from "sed -rne '<script>'" on the dpkg-parsechangelog line of a file.
extract_sed() {
    grep 'dpkg-parsechangelog' "$1" | sed -n "s/.*sed -rne '\\([^']*\\)'.*/\\1/p"
}
version_of() { # version_of SCRIPT_EXPR VERSION: what VERSION becomes
    printf 'Version: %s\n' "$2" | sed -rne "$1"
}

configured=$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$repo/configs/environ.json")
if [[ $configured =~ ^[0-9]+\.[0-9]+\.[0-9]+\+zelcore\.[1-9][0-9]*$ ]]; then
    ok "configs/environ.json version '$configured' is <base>+zelcore.<N>"
else
    bad "configs/environ.json version '$configured' is not <base>+zelcore.<N>"
fi
# Version ends up in file names, a make variable and a Go -ldflags -X value.
if [[ $configured =~ ^[A-Za-z0-9.+]+$ ]]; then ok "version has only filename/ldflags-safe characters"; else bad "version has unsafe characters"; fi

for f in build/docker/deb/build-deb.sh contrib/scripts/deploy-dev.sh; do
    expr=$(extract_sed "$repo/$f")
    [[ -n $expr ]] || { bad "$f: no dpkg-parsechangelog sed expression found"; continue; }
    eq "$f keeps the +zelcore suffix" "0.6.0+zelcore.1" "$(version_of "$expr" "0.6.0+zelcore.1")"
    eq "$f keeps a multi-digit N" "0.6.0+zelcore.12" "$(version_of "$expr" "0.6.0+zelcore.12")"
    eq "$f keeps a plain upstream version" "0.6.0" "$(version_of "$expr" "0.6.0")"
    eq "$f strips only a Debian revision" "0.6.0+zelcore.1" "$(version_of "$expr" "0.6.0+zelcore.1-2")"
done

if command -v dpkg >/dev/null; then
    lt() { dpkg --compare-versions "$1" lt "$2"; }
    if lt 0.5.0 0.6.0 && lt 0.6.0 0.6.0+zelcore.1 && lt 0.6.0+zelcore.1 0.6.0+zelcore.2 \
        && lt 0.6.0+zelcore.2 0.6.0+zelcore.10 && lt 0.6.0+zelcore.10 0.6.1; then
        ok "dpkg ordering 0.5.0 < 0.6.0 < 0.6.0+zelcore.1 < .2 < .10 < 0.6.1"
    else
        bad "dpkg ordering"
    fi
else
    echo "skip - dpkg not installed, ordering not checked"
fi

echo "$passed passed, $failed failed"
[[ $failed -eq 0 ]]
