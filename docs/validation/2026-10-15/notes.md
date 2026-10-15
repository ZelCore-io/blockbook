# Local Blockbook toolchain — 2026-10-15

Scope: scratch assets only; no repo modifications, integration runs, SSH, Ansible, production calls, sudo, or Docker reset.

Baseline source: `b14641d979241b93108025f2a72b4517ab4db260` from `/tmp/zelcore-blockbook-20261015`; clean immutable baseline extraction will live in this scratch directory.

Host: Darwin arm64, Apple clang 21, 14 CPUs, Go 1.26.5 installed. Pinned Go 1.23.7 downloaded through `GOTOOLCHAIN`.

Docker.app is installed but daemon unavailable. `open -a Docker` started backend processes; `docker desktop start --timeout 30` timed out. Supervisor repeatedly reports `unmarshaling start request: unexpected EOF`. No settings reset attempted. Native fallback chosen.

Native source archives (downloaded from primary upstream GitHub releases/tags):

- RocksDB v9.10.0: https://github.com/facebook/rocksdb/archive/refs/tags/v9.10.0.tar.gz — SHA256 `fdccab16133c9d927a183c2648bcea8d956fb41eb1df2aacaa73eb0b95e43724`
- ZeroMQ 4.3.5: https://github.com/zeromq/libzmq/releases/download/v4.3.5/zeromq-4.3.5.tar.gz — SHA256 `6653ef5910f17954861fe72332e68b03ca6e4d9c7160eb3a8de5a5a913bfab43`

These hashes record the fetched archives; no independent signed checksums were supplied.

RocksDB static build uses existing Homebrew LZ4 1.10.0 and Zstd 1.5.7 plus system zlib/bzip2. Snappy, GFlags and Jemalloc disabled. Blockbook sets LZ4HC compression in `db/dboptions.go`. This supports local correctness tests; it is not a production Linux artifact or exact production link configuration.

Builds running; logs under `logs/`. Source `env.sh` for reusable pinned Go/native cgo environment. `run-unit-tests.sh REPOSITORY [args]` selects exactly the non-contrib/non-integration Go packages used by the old Docker unit target, with cache disabled and ten minute per-package timeout.

Native dependencies built and installed successfully; pkg-config reports RocksDB 9.10.0 and ZeroMQ 4.3.5. The official grocksdb `grocksdb_no_link` build tag is required to use the native macOS flags in env.sh instead of its default Linux-oriented `-lstdc++ -ldl -lsnappy` flags. The unit runner includes this tag alongside `unittest`.

Reusable commands:

```sh
source /tmp/blockbook-toolchain-20261015/env.sh
cd /tmp/zelcore-blockbook-20261015
go test -tags unittest,grocksdb_no_link -count=1 ./server
/tmp/blockbook-toolchain-20261015/run-unit-tests.sh /tmp/zelcore-blockbook-20261015
```

Use `-tags grocksdb_no_link` for ordinary native builds. Native dependencies are static archives, compression libs from existing Homebrew installations are dynamically linked. No Linux production artifact is produced by these commands.

## Outcomes

- `logs/baseline-unit.log`: initial default grocksdb link attempt failed for api/db/fiat/fourbyte/server with missing Snappy. Corrected using the package's supported `grocksdb_no_link` tag; no dependency source modifications.
- `logs/baseline-unit-native.log`: full clean b146 unit suite PASSED, process exit 0, 47 package `ok` lines, including api, db, server, fiat and fourbyte. Source was extracted with `git archive` before concurrent implementation edits. No integration build tag or network integration suite was used.
- `logs/baseline-build.log`: baseline main binary built successfully, process exit 0, at `bin/blockbook-baseline`, embedding version 0.5.0 and full baseline SHA.

Build commands (after unpacking the downloaded archives):

```sh
cd /tmp/blockbook-toolchain-20261015/src/zeromq-4.3.5
./configure --prefix=/tmp/blockbook-toolchain-20261015/prefix --disable-shared --enable-static --without-libsodium --disable-curve --disable-drafts --disable-Werror
make -j8
make install

cd /tmp/blockbook-toolchain-20261015/src/rocksdb-9.10.0
export ROCKSDB_DISABLE_SNAPPY=1 ROCKSDB_DISABLE_GFLAGS=1 ROCKSDB_DISABLE_JEMALLOC=1
export CFLAGS='-fPIC -I/opt/homebrew/opt/lz4/include -I/opt/homebrew/opt/zstd/include'
export CXXFLAGS="$CFLAGS"
export LDFLAGS='-L/opt/homebrew/opt/lz4/lib -L/opt/homebrew/opt/zstd/lib'
make -j8 static_lib DEBUG_LEVEL=0 PORTABLE=1 DISABLE_WARNING_AS_ERROR=1
make install-static DEBUG_LEVEL=0 PORTABLE=1 DISABLE_WARNING_AS_ERROR=1 PREFIX=/tmp/blockbook-toolchain-20261015/prefix
```

The baseline production-source extraction remains unchanged; a scratch-only `db/release_fixture_test.go` was subsequently injected to compile `bin/baseline-db.test`. Compile command:

```sh
source /tmp/blockbook-toolchain-20261015/env.sh
cd /tmp/blockbook-toolchain-20261015/baseline
go test -c -tags unittest,grocksdb_no_link -o /tmp/blockbook-toolchain-20261015/bin/baseline-db.test ./db
```

`run-db-rehearsal.sh REPOSITORY COMMITTED_CANDIDATE_REVISION` extracts the committed candidate into a fresh scratch directory, injects the same harness, compiles a candidate DB test executable, and runs baseline-create → candidate-reuse/advance → baseline-read/disconnect → candidate-reopen checks with stopped copies between processes. It emits a per-run report and preserves all logs and databases. It never edits the supplied repository or opens an existing production DB. Its scope is explicitly Bitcoin-testnet-fixture/native-library correctness only. Real production snapshots and Linux runtime qualification remain unavailable here.

## Branding candidate qualification

Candidate `a3bcc18a774d0122ff15832ee79763b34973a406`: cross-binary fixture rehearsal PASSED in `rehearsal-a3bcc18a774d-gk1ejp/report.md`, all four subprocesses exited zero. Logs confirm schema version 7, exact expected tip hashes, successful candidate advancement and baseline rollback followed by candidate reopen. No native Insight changes exist in this candidate.

Branding-only patch applicability PASSED against both full recorded GRS bases (`9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6` and `dcdb2bd25a36da6e5d90a148cc183692c4e14ef6`). Reproducible scratch-index checker: `check-branding-backports.sh`. Report and all staged index/object artifacts: `branding-backports-a3bcc18a-iTchtx/report.md`. The checker only applies the branding parent-to-commit patch, preserving each base's other contents. This proves clean application, not runtime compatibility of GRS releases. No working repository or host was changed.

## Native Insight candidate qualification

Candidate `dda65ea2ee6ddce23966c2804483b6337f3ad771`: cross-binary fixture rehearsal PASSED in `rehearsal-dda65ea2ee6d-DWpOtn/report.md`, all four subprocesses exited zero. Candidate reopening/advance, baseline reopening/rollback and candidate reopening/restoration all passed using the same Bitcoin fixtures/native libraries and the same production-snapshot limitations. This is database fixture compatibility evidence; it does not exercise the Insight HTTP contract.

Cumulative baseline-to-candidate patch applicability PASSED against both recorded GRS bases. Report/artifacts: `release-backports-dda65ea2ee6d-LDCGnp/report.md`; reproducible checker: `check-release-backports.sh REPOSITORY CANDIDATE_REVISION`. The cumulative patch includes the two design/scope documentation commits, branding and native Insight; it does not claim that cherry-picking only the two implementation commits supplies every prerequisite. Source working tree/configs remained untouched. No broad suites were repeated or hosts contacted.
