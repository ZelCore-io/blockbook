# Retained reproduction evidence

See the [validation record](../../fork-forward-port-validation-2026-10-15.md) for
source/result mapping and scope. These files preserve compact local evidence after
scratch reports are removed. They contain no binaries, caches, databases, credentials
or raw test logs. Commands below are a reproduction recipe, not a claim they were
rerun for the documentation commit. Use an isolated checkout/archive at the stated SHA.
Do not run integration, host or deployment commands as part of this recipe.

- [native-env.sh](native-env.sh): exact environment used on the original Darwin host.
  Its `/tmp` and Homebrew paths are historical scratch locations, not provisioned assets
  or portable defaults. Recreate matching native dependencies and adjust paths first.
- [release-guard-validation.py](release-guard-validation.py): original fixture-based
  guard/build-forwarding checks, with only source-root discovery made relative to this
  file. Uses fake Docker and disposable Git repositories; invokes no real container,
  package builder or daemon. Run with `python3 docs/validation/2026-10-15-forward-port/release-guard-validation.py`.
- [preview-harness.go.txt](preview-harness.go.txt): original fixture-backed preview test.
  The `.txt` suffix keeps it out of Go package discovery. It is retained as an input,
  not a production test added by this documentation change.

## Native commands and recorded results

On the original host, source `native-env.sh`; verify `go version` is Go 1.25.4,
`bash --version` is 5.3 and the native library headers/linker inputs exist. Modules
were downloaded before switching offline. Use `GOPROXY=off GOSUMDB=off` for each
command. Run from an immutable archive/isolated checkout of the relevant source.

At upstream `b427738facd3a8c4cf3986e6e11d868a214deeea`:

```sh
GOPROXY=off GOSUMDB=off bash contrib/tests/run-unit-tests.sh -tags unittest,grocksdb_no_link -count=1 -timeout 10m
GOPROXY=off GOSUMDB=off go build -tags grocksdb_no_link -o /tmp/blockbook-upstream-baseline .
GOPROXY=off GOSUMDB=off go test -race -tags unittest,grocksdb_no_link -count=1 -timeout 10m -run '^(TestGoRuntimeMetricsExported|TestEthereumClientHeaderByNumberKeepsNodeHash|TestEthereumNewBlockKeepsNodeHash|TestEthereumHeaderWithoutHash)$' ./common ./bchain/coins/eth
```

Recorded: each exited 0; 50 unit packages passed, 15 without tests; both focused
race packages passed. The archive SHA256 was
`d63060978572b3ea8bd6154cc1166bd21ad424e1c5e4597b3b774556b04cc6d2`.
All 903 archived regular source files remained byte-identical after testing.

At combined source `147edd6170c323813cd50fbde6a4a37ecd9000ee`:

```sh
GOPROXY=off GOSUMDB=off bash contrib/tests/run-unit-tests.sh -tags unittest,grocksdb_no_link -count=1 -timeout 10m
GOPROXY=off GOSUMDB=off go test -tags unittest,grocksdb_no_link . ./server ./build/tools -count=1 -timeout=5m
GOPROXY=off GOSUMDB=off go test -race -tags unittest,grocksdb_no_link . ./server ./build/tools -run 'TestSignalShutdownDrainsInsightBeforeDaemon|TestInsight' -count=1 -timeout=5m
GOPROXY=off GOSUMDB=off go build -tags grocksdb_no_link -ldflags='-X github.com/trezor/blockbook/common.gitcommit=147edd6170c323813cd50fbde6a4a37ecd9000ee -X github.com/trezor/blockbook/common.buildtime=2026-10-15T00:00:00Z' -o /tmp/blockbook-combined .
go version -m /tmp/blockbook-combined
/tmp/blockbook-combined -h
GOPROXY=off GOSUMDB=off go test -tags unittest,grocksdb_no_link -ldflags='-X github.com/trezor/blockbook/common.gitcommit=147edd6170c323813cd50fbde6a4a37ecd9000ee' ./server -run 'TestInsightCorrespondingSourceHeader|TestSourceLink' -count=1 -timeout=2m
make check-release-source
```

Recorded: each succeeded; 51 unit packages passed; focused normal root/server/tools
passed in 1.201/4.111/1.468s and focused race in 4.652/4.931/4.881s. Committed-source
server tests passed in 0.480s. Metadata showed Go1.25.4 and the exact full source SHA.
There were no Go `vcs.*` build-info fields; clean checkout and source guard were checked
separately. Help displayed the optional Insight flag and exited before startup.
The original output paths were `/tmp/task4-blockbook` and
`/tmp/task4-blockbook-committed`; the output path above is a reproduction convenience.

Branding source tests also passed at exact commit
`3624fa8e4230193599be56d532350112fb8b9fda`, using its SHA in linker metadata and
selector `TestSourceLink|TestExplorerSourceRendering`. Affected server/API/DB tests
passed in 7.136/8.315/11.118s. Native source-link negative control used a Go overlay
that always generated `/tree/<identifier>`; invalid/unknown/dirty cases failed as
expected. Broadcast negative control replaced the current narrow closure with the
old direct method argument; compilation failed on `func(string,bool)` versus
`func(string)` as expected. Real source files were not changed by either overlay.

## Preview reproduction

In a disposable checkout of branding source, copy `preview-harness.go.txt` to
`server/task3_preview_test.go`. The harness uses existing server test fixtures.
Set `TASK3_PREVIEW_VARIANT=release` or `dirty` and run the following, replacing
`<revision>` with the clean 40-hex SHA or its `-dirty` variant:

```sh
TASK3_PREVIEW_VARIANT=release GOPROXY=off GOSUMDB=off go test -tags unittest,grocksdb_no_link -ldflags='-X github.com/trezor/blockbook/common.gitcommit=<revision>' ./server -run '^TestTask3Preview$' -count=1
```

It writes status/address/transaction HTML for debug false/true beneath the test
package's `.superpowers/sdd/2026-10-15-fork-release-management/preview/` directory.
Remove the copied test after generation; it dirties that disposable checkout.
Inspect generated pages in an isolated loopback preview with their repository assets.
Historical browser inspection used injected `cea776de` metadata and its dirty variant;
exact committed branding/native metadata was checked separately. No saved screenshot
is retained or claimed. Native/real-worker/Linux/production limitations in the
validation record apply to all these commands.

## Documentation-only verification

From the candidate tree, check the unchanged upstream subsystems and shell syntax:

```sh
git diff --exit-code b427738facd3a8c4cf3986e6e11d868a214deeea -- db bchain go.mod go.sum configs/coins
bash -n build/docker/deb/build-deb.sh
bash -n docs/validation/2026-10-15-forward-port/native-env.sh
git diff --check
```

All 12 relative Markdown targets and retained Python syntax were checked locally.
The retained release-guard fixture script was rerun successfully after making its
source-root discovery relative; all clean/dirty/status-error/metadata/environment
checks passed using fake Docker. Both shell syntax checks and subsystem invariance
passed, as did patch whitespace checks. Go suites were not repeated for these documentation edits.
