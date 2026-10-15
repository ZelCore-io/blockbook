#!/usr/bin/env bash
# Match the deployed base's make test package selection, without Docker.
set -euo pipefail
source /tmp/blockbook-toolchain-20261015/env.sh
repo=${1:?usage: run-unit-tests.sh REPOSITORY [go test arguments]}
shift
cd "$repo"
packages=()
while IFS= read -r package; do
  packages+=("$package")
done < <(go list ./... | grep -vE '^github\.com/trezor/blockbook/(contrib|tests)')
if [[ ${#packages[@]} -eq 0 ]]; then
  echo 'No unit-test packages resolved.' >&2
  exit 1
fi
exec go test -tags unittest,grocksdb_no_link -count=1 -timeout 10m "${packages[@]}" "$@"
