#!/usr/bin/env bash
# Run inside an unprivileged disposable Linux container; swap utilities are mocked.
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
export PATH="$work/bin:$PATH" TEST_LOG="$work/log" BLOCKBOOK_SWAP_MIB=1 BLOCKBOOK_SWAP_RESERVE_MIB=1024
cat > "$work/bin/df" <<'MOCK'
#!/bin/bash
printf 'Avail\n%s\n' "${TEST_FREE_BYTES:-10737418240}"
MOCK
cat > "$work/bin/findmnt" <<'MOCK'
#!/bin/bash
echo "${TEST_FSTYPE:-ext4}"
MOCK
cat > "$work/bin/awk" <<'MOCK'
#!/bin/bash
if [[ ${TEST_LOW_RAM:-0} == 1 ]]; then
 if [[ ${*: -1} == /proc/meminfo ]]; then echo 0; exit 0; fi
 if [[ ${*: -1} == /proc/swaps && $* == *'print $4'* ]]; then echo 1048576; exit 0; fi
fi
exec /usr/bin/awk "$@"
MOCK
cat > "$work/bin/dd" <<'MOCK'
#!/bin/bash
if [[ ${TEST_DD_EXIT:-0} != 0 ]]; then
 for arg in "$@"; do if [[ $arg == of=* ]]; then echo partial > "${arg#of=}"; fi; done
 exit "$TEST_DD_EXIT"
fi
exec /usr/bin/dd "$@"
MOCK
cat > "$work/bin/id" <<'MOCK'
#!/bin/bash
echo "${TEST_UID:-0}"
MOCK
for cmd in mkswap swapon swapoff make; do
 cat > "$work/bin/$cmd" <<'MOCK'
#!/bin/bash
name=${0##*/}
printf '%s %s\n' "$name" "$*" >> "$TEST_LOG"
case "$name" in
 mkswap) exit "${TEST_MKSWAP_EXIT:-0}";;
 swapon) exit "${TEST_SWAPON_EXIT:-0}";;
 swapoff) exit "${TEST_SWAPOFF_EXIT:-0}";;
 make) if [[ -n ${TEST_SIGNAL:-} ]]; then kill -s "$TEST_SIGNAL" "$PPID"; fi; exit "${TEST_BUILD_EXIT:-0}";;
esac
MOCK
done
chmod +x "$work/bin/"*
run_case() {
 local name=$1 expected=$2; shift 2
 : > "$TEST_LOG"
 set +e
 env "$@" bash "$repo/contrib/scripts/build-with-swap.sh" groestlcoin > "$work/output" 2>&1
 actual=$?
 set -e
 if [[ $actual != "$expected" ]]; then cat "$work/output"; echo "$name: exit $actual != $expected"; exit 1; fi
 echo "PASS $name"
}
run_case success 0
grep -q '^make release-deb-blockbook-groestlcoin PORTABLE=1 BB_RPC_ENV=-e GOFLAGS="-p=1 -timeout=45m"$' "$TEST_LOG"
grep -q '^swapoff ' "$TEST_LOG"
file=$(awk '$1=="swapon" {print $2}' "$TEST_LOG")
[[ ! -e $file && ! -d ${file%/*} ]]
run_case interruption 143 TEST_SIGNAL=TERM
grep -q '^swapoff ' "$TEST_LOG"
file=$(awk '$1=="swapon" {print $2}' "$TEST_LOG")
[[ ! -e $file ]]
run_case partial-allocation 23 TEST_DD_EXIT=23
[[ ! -s $TEST_LOG ]]
[[ -z $(find /var/tmp -maxdepth 1 -type d -name 'blockbook-build-swap.*' -print -quit) ]]
run_case non-root 1 TEST_UID=1000
[[ ! -s $TEST_LOG ]]
run_case build-failure 17 TEST_BUILD_EXIT=17
grep -q '^swapoff ' "$TEST_LOG"
run_case disk-shortage 1 TEST_FREE_BYTES=1024
[[ ! -s $TEST_LOG ]]
run_case unsupported-filesystem 1 TEST_FSTYPE=btrfs
[[ ! -s $TEST_LOG ]]
run_case invalid-size 1 BLOCKBOOK_SWAP_MIB=-1
[[ ! -s $TEST_LOG ]]
run_case mkswap-failure 18 TEST_MKSWAP_EXIT=18
! grep -q '^make\|^swapoff' "$TEST_LOG"
run_case swapon-failure 19 TEST_SWAPON_EXIT=19
! grep -q '^make\|^swapoff' "$TEST_LOG"
file=$(awk '$1=="swapon" {print $2}' "$TEST_LOG")
[[ ! -e $file ]]
run_case swapoff-failure 1 TEST_SWAPOFF_EXIT=20
file=$(awk '$1=="swapon" {print $2}' "$TEST_LOG")
[[ -f $file ]]
grep -q 'Manual cleanup' "$work/output"
rm -f "$file"; rmdir "${file%/*}"
run_case low-ram-cleanup 1 TEST_LOW_RAM=1
file=$(awk '$1=="swapon" {print $2}' "$TEST_LOG")
[[ -f $file ]]
! grep -q '^swapoff ' "$TEST_LOG"
rm -f "$file"; rmdir "${file%/*}"
run_case preserve-build-exit 17 TEST_BUILD_EXIT=17 TEST_SWAPOFF_EXIT=20
file=$(awk '$1=="swapon" {print $2}' "$TEST_LOG")
[[ -f $file ]]
rm -f "$file"; rmdir "${file%/*}"
# The lock covers both the build and cleanup.
(exec 9>/run/blockbook-build-swap.lock; flock -n 9; run_case concurrent-run 1; [[ ! -s $TEST_LOG ]])
echo 'All temporary-swap wrapper tests passed (no real swap enabled).'
