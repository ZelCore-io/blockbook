#!/usr/bin/env bash
set -euo pipefail
source /tmp/blockbook-toolchain-20261015/env.sh
repo=${1:?usage: run-db-rehearsal.sh REPOSITORY CANDIDATE_REVISION}
revision=${2:?usage: run-db-rehearsal.sh REPOSITORY CANDIDATE_REVISION}
candidate_sha=$(git -C "$repo" rev-parse --verify "$revision^{commit}")
baseline_sha=b14641d979241b93108025f2a72b4517ab4db260
run_dir=$(mktemp -d "$BB_TOOLCHAIN_ROOT/rehearsal-${candidate_sha:0:12}-XXXXXX")
printf '%s\n' "$run_dir"

mkdir "$run_dir/candidate-source"
git -C "$repo" archive "$candidate_sha" | tar -x -C "$run_dir/candidate-source"
cp "$BB_TOOLCHAIN_ROOT/release_fixture_test.go" "$run_dir/candidate-source/db/release_fixture_test.go"
(cd "$run_dir/candidate-source" && go test -c -tags unittest,grocksdb_no_link -o "$run_dir/candidate-db.test" ./db) > "$run_dir/candidate-compile.log" 2>&1

if [[ ! -x "$BB_TOOLCHAIN_ROOT/bin/baseline-db.test" ]]; then
  echo 'Compile baseline-db.test using instructions in notes.md first.' >&2
  exit 1
fi

BB_RELEASE_FIXTURE_PATH="$run_dir/original-db" BB_RELEASE_FIXTURE_MODE=create "$BB_TOOLCHAIN_ROOT/bin/baseline-db.test" -test.run '^TestReleaseDatabaseFixture$' -test.v -test.timeout 2m > "$run_dir/01-baseline-create.log" 2>&1
cp -R "$run_dir/original-db" "$run_dir/candidate-db"
BB_RELEASE_FIXTURE_PATH="$run_dir/candidate-db" BB_RELEASE_FIXTURE_MODE=advance "$run_dir/candidate-db.test" -test.run '^TestReleaseDatabaseFixture$' -test.v -test.timeout 2m > "$run_dir/02-candidate-reuse-advance.log" 2>&1
cp -R "$run_dir/candidate-db" "$run_dir/rollback-db"
BB_RELEASE_FIXTURE_PATH="$run_dir/rollback-db" BB_RELEASE_FIXTURE_MODE=rollback "$BB_TOOLCHAIN_ROOT/bin/baseline-db.test" -test.run '^TestReleaseDatabaseFixture$' -test.v -test.timeout 2m > "$run_dir/03-baseline-read-rollback.log" 2>&1
BB_RELEASE_FIXTURE_PATH="$run_dir/rollback-db" BB_RELEASE_FIXTURE_MODE=verify-rollback "$run_dir/candidate-db.test" -test.run '^TestReleaseDatabaseFixture$' -test.v -test.timeout 2m > "$run_dir/04-candidate-reopen-rollback.log" 2>&1

cat > "$run_dir/report.md" <<EOF
# Offline database fixture rehearsal

Result: PASS (all four test processes exited successfully).

- Baseline source: $baseline_sha
- Candidate source: $candidate_sha
- Native platform: Darwin arm64; Go 1.23.7; RocksDB 9.10.0; ZeroMQ 4.3.5.
- Baseline created fixture block 225493 and closed gracefully.
- Candidate reopened a copy, checked fixture columns, connected block 225494, checked updated columns and closed.
- Baseline reopened a candidate-written copy, checked columns, disconnected 225494, checked restored columns and closed.
- Candidate reopened the rollback copy and checked restored block 225493 state.

The fixture helper checks the height, address, transaction-address, balance/UTXO, and block-transaction indexes against expected bytes. All processes also check the exact expected tip hash and a gracefully closed persisted internal state. Schema version is checked by LoadInternalState.

Scope: upstream Bitcoin testnet fixtures only, using small temporary databases and a common native library build. This does not qualify actual BTG/GRS production database snapshots, Linux runtime libraries, network catch-up, resource capacity or live rollback. No production snapshot was available. No network integration suite was run.

Logs: 01-baseline-create.log, 02-candidate-reuse-advance.log, 03-baseline-read-rollback.log, 04-candidate-reopen-rollback.log. Harness: $BB_TOOLCHAIN_ROOT/release_fixture_test.go. All DBs were copied only after the preceding process exited.
EOF
cat "$run_dir/report.md"
