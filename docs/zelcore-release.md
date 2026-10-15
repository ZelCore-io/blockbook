# Zelcore Blockbook branding release

This fork starts from deployed Blockbook 0.5.0 revision
`b14641d979241b93108025f2a72b4517ab4db260`. The branding change preserves the
existing database, parsers, module versions, backend settings, Terms of Use,
SatoshiLabs attribution, and AGPL notices in COPYING. It adds Zelcore,
documentation, Flux, and corresponding-source links to the existing explorer.

## Choose the release scope

The recommended first release is branding only:
`codex/blockbook-branding-0.5` at
`a3bcc18a774d0122ff15832ee79763b34973a406`. The separately reviewable
`codex/blockbook-insight-0.5` stacks the optional listener at
`dda65ea2ee6ddce23966c2804483b6337f3ad771`. Later documentation commits
record evidence without extending runtime behavior. Use a clean separate checkout
of the selected revision for builds; the release targets embed that checkout's
actual full HEAD, including any later commits.

The [15 October validation record](zelcore-validation-2026-10-15.md) records
native builds, unit/race checks, offline Bitcoin fixtures and GRS patch
applicability. This is a locally verified candidate, with Linux packaging and
production qualification still outstanding. The optional
[Insight profile](insight-compatibility.md) uses the same database owner, is
disabled by default, and needs separate wallet/daemon acceptance before use.

## Build and revision metadata

Use the existing Docker toolchain on a working Docker host. The repository's
build image pins Go 1.23.7 and RocksDB 9.10.0; modules are downloaded from the
checkout's go.mod/go.sum during prepare-sources. The image no longer preloads an
unpinned upstream checkout. This does not make apt or image inputs reproducible;
record the resolved image and native library versions when qualifying a release.

Commit all intended source first. Both release targets reject tracked changes
and untracked files, then use `git rev-parse HEAD` to override any supplied
GITCOMMIT with the full source revision:

```sh
make release-build
# Or build only the Blockbook package for an existing coin definition:
make release-deb-blockbook-bgold
```

The Debian command builds the Blockbook package only. It does not rebuild or
upgrade a backend daemon. Do not use `deb-bgold`, `all`, or a backend target for
this branding release. Keep output artifacts outside the source checkout or
clean them before a subsequent release command. Git-ignored files are excluded
from the clean-source check; ensure local ignored files cannot replace required
source or alter the build inputs when qualifying the build.

Ordinary development commands `make build`, `make build-debug`, and
`make deb-blockbook-bgold` default to the full HEAD revision with a `-dirty`
suffix when tracked changes or untracked files exist. They accept explicit
`GITCOMMIT=<revision>` and forward it through the Docker binary and Debian
package build paths. Such an override is an assertion by the builder; use the
release targets for clean release metadata. Direct container builds also accept
GITCOMMIT; build-deb.sh exports it into the package's make invocation.

A clean full 40-hex revision produces a visible
`https://github.com/ZelCore-io/blockbook/tree/<revision>` source link on every
explorer page and on the status page's commit. Unknown, short, invalid, or dirty
identifiers use the fork repository root labelled “Source (development build)”.
The status page labels development revisions as well. No revision is stripped
from a dirty identifier to imply an exact source match.

## Publish corresponding source before public service

Before making a built service public, publish the matching source commit in
`https://github.com/ZelCore-io/blockbook` and verify the exact `/tree/<revision>`
URL is available without authentication. Publish the build scripts and required
corresponding source, including any local modifications and notices, and retain
COPYING and upstream attribution with distributed artifacts. Check the deployed
binary's status/version metadata against that published commit. A local,
unpublished development build is not evidence of AGPL compliance.

## Local verification and manual qualification

On this old baseline, the current repository's contrib test wrapper scripts do
not exist. Run equivalent local Go tests using the pinned Go/native library
versions; do not run production connectivity tests as part of local changes.

```sh
go test -tags unittest -count=1 ./server ./db
bash -n build/docker/deb/build-deb.sh
make -n build GITCOMMIT="$(git rev-parse HEAD)"
make -n deb-blockbook-bgold GITCOMMIT="$(git rev-parse HEAD)"
```

If the native toolchain supplies custom RocksDB linker flags, use its documented
`grocksdb_no_link` tag. Tests exercise production minification and debug template
parsing and the exact/fallback source targets. Inspect a locally rendered status,
transaction, and address page at desktop and mobile widths: verify coin label,
search, blocks/status navigation, currency controls, send-transaction link,
ecosystem links, terms, attribution, and the always-visible source link. Check
both a release revision and a dirty development build. Confirm assets remain
local and no external scripts or trackers were added.

Before a user-run canary, qualify a consistent production database snapshot with
the chosen binary and exact pinned native libraries, reopen/catch up that clone,
exercise daemon and wallet contracts, rehearse recovery, and compare metadata and
source availability. A matching schema number or fixture suite cannot establish
production database compatibility. Port allocation, production integration,
source publication, snapshot qualification, deployment and rollback remain
operator tasks; this change does not perform them. Keep branding independently
reviewable from the optional Insight listener work.


## GRS branding backports

The branding commit's parent-to-commit patch applies cleanly to both recorded
GRS bases, `9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6` and
`dcdb2bd25a36da6e5d90a148cc183692c4e14ef6`. In a separate local branch
based on the intended GRS revision, apply only the branding implementation:

```sh
git cherry-pick a3bcc18a774d0122ff15832ee79763b34973a406
```

Resolve and review that branch independently, rerun its checks, and build with
its new full HEAD. Do not embed the BTG branding SHA into a different GRS tree.
The older base retains backend package metadata for GRS 28.0; the newer retains
29.0 (including their original binary URLs/checksums). The branding patch does
not upgrade either daemon. Applicability was checked with scratch Git indexes;
no GRS binary, daemon, package or production database was qualified. The
cumulative optional-Insight patch also applies; its precise scope and the
retained checkers are in the validation record. Do not replace either deployed
base with current upstream master to obtain these changes.
