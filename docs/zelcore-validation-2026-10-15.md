# Local release validation — 15 October 2026

The branding candidate and optional native Insight candidate passed the local
checks below. This record qualifies native builds and isolated Bitcoin fixtures;
Linux/Debian artifacts, production databases, BTG/GRS daemon integration and real
wallet use remain unqualified. Nothing was deployed, pushed, merged or published.
Independent implementation reviews for Tasks 1 and 2 reported no Critical or
Important findings.

## Source identity and separate candidates

| Role | Branch / revision |
| --- | --- |
| Deployed BTG baseline, Blockbook 0.5.0 | `b14641d979241b93108025f2a72b4517ab4db260` |
| Branding only | `codex/blockbook-branding-0.5`, `a3bcc18a774d0122ff15832ee79763b34973a406` |
| Optional Insight stacked on branding | `codex/blockbook-insight-0.5`, `dda65ea2ee6ddce23966c2804483b6337f3ad771` |
| Older recorded GRS base | `9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6` |
| Newer recorded GRS base | `dcdb2bd25a36da6e5d90a148cc183692c4e14ef6` |

The branding-only branch is preserved independently. Later documentation-only
commits on the stacked branch record these runtime candidates. The
[release guide](zelcore-release.md) describes choosing a scope, clean release
commands and exact corresponding-source publication. The optional
[Insight profile](insight-compatibility.md) remains disabled by default: same DB
owner, one address, first 1000 history positions, no Socket.IO, and all five
routes returning 503 until ready (OPTIONS remains available). Port allocation
and proxy binding are operator prerequisites.

The following check was repeated on the final runtime candidate and again while
preparing this documentation; output was empty. `db/rocksdb.go` still declares
`const dbVersion = 7`:

```sh
git diff b14641d979241b93108025f2a72b4517ab4db260 -- db bchain go.mod go.sum
rg -n '^const dbVersion = ' db/rocksdb.go
```

This establishes unchanged DB/parser/module source. It does not prove that a
particular deployed DB, runtime library set or daemon is compatible.

## Native toolchain and builds

Host: Darwin arm64, Apple clang 21. Pinned Go 1.23.7, RocksDB 9.10.0,
ZeroMQ 4.3.5; Homebrew LZ4 1.10.0 and Zstd 1.5.7, system zlib/bzip2.
Snappy, GFlags and Jemalloc were disabled for the local RocksDB build.
[Retained toolchain notes](validation/2026-10-15/notes.md) include upstream
archive URLs, recorded SHA256 hashes and native build commands. These hashes
record downloads; no independently signed checksum verification was supplied.

Docker's daemon was unavailable. No Docker image, Linux executable or Debian
package was built; no Docker reset was attempted. Native libraries and
compression linkage differ from production Linux. Existing duplicate-library
linker warnings occurred; race linking also emitted a Darwin `LC_DYSYMTAB`
warning. Executed checks exited successfully despite those warnings.

The local environment was sourced from
`/tmp/blockbook-toolchain-20261015/env.sh`. Its
[retained copy](validation/2026-10-15/env.sh) shows the flags. This historical
scratch path is a toolchain location, not the release repository destination.
Native checks use the supported `grocksdb_no_link` tag to supply Darwin linker
flags; ordinary Linux toolchains need their appropriate linkage instead.

Both baseline and branding native main binaries built. The branding binary
embedded the exact branding SHA, verified with `go version -m`; its build was:

```sh
source /tmp/blockbook-toolchain-20261015/env.sh
go build -tags grocksdb_no_link \
  -ldflags='-X github.com/trezor/blockbook/common.version=0.5.0 -X github.com/trezor/blockbook/common.gitcommit=a3bcc18a774d0122ff15832ee79763b34973a406 -X github.com/trezor/blockbook/common.buildtime=2026-10-15T00:00:00Z' \
  -o /tmp/blockbook-toolchain-20261015/bin/blockbook-branding .
```

Run a candidate build in its corresponding source checkout. The stacked
candidate separately passed `go build -tags grocksdb_no_link -o
/tmp/blockbook-insight-20261015 .` and `-h` displayed the disabled-by-default
`-insight string` option without connecting to a daemon. That ordinary native
build used development metadata; it is compile evidence, not a published release
artifact. Qualify an actual release using the clean targets in the release guide.

## Recorded test evidence

| Stage | Check and result |
| --- | --- |
| Immutable baseline extraction | Native unit suite: 47 tested packages passed; native main build passed |
| Branding | Native unit suite: 47 tested packages passed |
| Branding final covering check after footer fix | `go test -tags unittest,grocksdb_no_link -count=1 ./server ./db`: passed (0.885s / 2.169s) |
| Branding revision/render contracts | Six source-link cases and six production/debug render subtests passed; clean-SHA and dirty-SHA ldflags runs passed; postcommit branding-SHA render passed (0.518s) |
| Build plumbing | Binary/debug/Debian/inner-make dry runs forwarded explicit SHA; `bash -n build/docker/deb/build-deb.sh` passed |
| Clean release guard | Disposable Git fixture/fake Docker: exact HEAD overrides supplied metadata, tracked/untracked changes rejected before Docker, development builds marked dirty; all passed |
| Insight full local unit suite | 61 selected packages, 49 with tests: passed |
| Insight final affected packages | `go test -tags unittest,grocksdb_no_link -count=1 . ./server ./build/tools`: passed (1.187s / 1.073s / 0.220s) |
| Insight final focused race run | `go test -race -tags unittest,grocksdb_no_link -count=1 . ./server ./build/tools -run 'TestInsight\|TestSignalShutdown'`: passed (2.393s / 1.979s / 1.238s), no race reports |
| Both runtime candidates | `git diff --check`: passed; DB/parser/module source diff empty |

The full Insight suite preceded final shutdown-timeout, source-header and error
classification refinements. All affected packages were rerun normally and with
focused race detection after those changes. The retained
[unit runner](validation/2026-10-15/run-unit-tests.sh) matches the old baseline's
non-contrib/non-integration package selection with cache disabled and a ten
minute package timeout. No broad suite was repeated for documentation edits.

These are implementation reports and local toolchain results, not fresh runs
of every command from this document. Red/green checks included runtime negative
controls for invalid source links, history offset enrichment, release guards,
shutdown timeout budgeting, source Link/CORS headers and sanitized errors.
Original detailed Task 1/2 reports remain in the local ignored SDD evidence.

The fake-worker race run exercises the new listener/lifecycle. Existing
`api/worker.go:2477` reads historical fiat timestamps updated in
`fiat/fiat_rates.go:462`; it does not establish race freedom of those inherited
real-worker paths. Startup readiness gating does not resolve that concern.
This is a separate qualification item; no core worker/fiat code changed.

## Offline database fixture rehearsal

Both [branding](validation/2026-10-15/branding-fixture.md) and
[stacked Insight](validation/2026-10-15/insight-fixture.md) rehearsals passed all
four processes: baseline creates block 225493 and closes; candidate reopens a
stopped copy and advances to 225494; baseline reopens a stopped candidate-written
copy and disconnects 225494; candidate reopens and verifies restored state.
Checks cover height/tip hash, address, transaction-address, balance/UTXO and
block-transaction columns and gracefully closed persisted state. Schema 7 is
checked by internal-state loading.

The [fixture harness](validation/2026-10-15/release_fixture_test.go.txt) is injected
only into scratch source extractions. It is retained with a `.txt` suffix so
normal Go package discovery excludes it.
The [rehearsal runner](validation/2026-10-15/run-db-rehearsal.sh) records the
process sequence. Reports and scripts retain their contents (trailing blank lines normalized) and
historical scratch paths. Reproduction requires provisioning that toolchain,
copying the harness to its stated scratch location as `release_fixture_test.go`
and compiling the baseline
DB test executable as documented in the notes; scripts are not portable
one-command setup installers. No DBs or binaries are committed.

These are upstream Bitcoin testnet fixtures with common native libraries. They
do not test BTG/GRS data, real network catch-up, live recovery, resource capacity,
Insight HTTP, production snapshots or Linux library compatibility.

## GRS patch application evidence

[Branding-only](validation/2026-10-15/branding-backports.md) and
[cumulative release](validation/2026-10-15/insight-backports.md) application
checks passed against both recorded GRS bases using isolated indexes in scratch
shared-object clones. `read-tree`, `apply --cached --check`, `apply --cached`,
`diff --cached --check` and `write-tree` all exited zero. No deployment or source
working tree was changed. Retained checkers:
[branding](validation/2026-10-15/check-branding-backports.sh) and
[cumulative](validation/2026-10-15/check-release-backports.sh).

The branding check uses only `a3bcc18a^..a3bcc18a`. The cumulative check uses
`b14641d9..dda65ea2`, including planning commits `80048820` and `3aaffbad`,
branding and Insight. It does not claim that cherry-picking only the two
implementation commits supplies every documentation prerequisite. Scratch
index application proves patch applicability; per-base builds/tests remain
outstanding. GRS 28.0 versus 29.0 backend version/URL/checksum metadata stays
inherited from the selected GRS base. Each backport needs its own full source
revision in build metadata and published matching source.

## Preview and remaining release checks

Actual production/debug status, transaction and address HTML was generated
locally, including clean-revision renders. Desktop and 390px mobile previews
were inspected by the controller. The footer now wraps with all links visible
(94.375px high on the measured mobile view). A legacy status-row/gutter overflow
of about 10px remains (400px layout at 390px viewport). Screenshot image content
was inspected through the browser tool, but filesystem screenshot export was
unavailable; no saved screenshots are claimed. No new external assets or
trackers were added.

Before public service or a user-run canary, complete:

1. Actual Linux/Debian builds with recorded native/image inputs and full release
   SHA; verify matching published source and required corresponding material.
2. Consistent copied production DB reopen, catch-up and recovery/rollback with
   the chosen binary/libraries. Never run two writers against one live DB.
3. BTG/GRS daemon acceptance/rejection and wallet balance, pending history,
   export and spending checks, including coinbase and OP_RETURN handling.
4. For optional Insight: allocate port/private interface, verify trusted proxy,
   TLS/CORS, readiness, saturation/body limits and active-request shutdown;
   assess inherited worker concurrency and legacy uncancellable calls.
5. User-run canary with a retained recoverable snapshot and explicit rollback
   procedure. Any unrelated write migration requires its pre-migration snapshot.

No Ansible, SSH, host edits, production connectivity tests, daemon upgrades,
source publication, deployment or canary occurred in this work.
