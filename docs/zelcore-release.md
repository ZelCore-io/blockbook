# Zelcore Blockbook branding and source metadata

The branding forward port is based on current upstream Blockbook revision
`b427738f` and preserves that baseline's database, parsers, module versions,
backend settings, Terms of Use, SatoshiLabs attribution, and AGPL notices in
COPYING. The earlier `release/zelcore-0.5` maintenance candidate is separate;
its local qualification does not qualify this current-master candidate.

## Build and revision metadata

The current Docker build pins Go 1.25.4 and RocksDB 9.10.0. Modules are downloaded
from the checkout's go.mod/go.sum during prepare-sources; the image does not
preload an unpinned upstream checkout. Existing BB_BUILD_ENV, BB_RPC_ENV,
BB_STAGING and static jemalloc build plumbing remains in place. Apt and image
inputs are not fully reproducible; record resolved images and native libraries
when qualifying a release.

Commit all intended source first. Release targets reject tracked changes and
untracked files, then override any supplied GITCOMMIT with the full HEAD SHA:

```sh
make release-build
# Build only Blockbook for an existing coin definition:
make release-deb-blockbook-bgold
```

The Debian command builds only the Blockbook package. Keep artifacts outside
the checkout or clean them before another release command. Git-ignored files
are excluded from the source guard; qualifying builders must ensure ignored
inputs cannot replace required source or alter the build.

Ordinary build, build-debug and deb-blockbook targets default to full HEAD with
a `-dirty` suffix for tracked or untracked changes. Explicit GITCOMMIT overrides
are supported through Docker binary/debug/Debian paths and exported by direct
build-deb.sh invocation. Such an override is the builder's assertion; use the
release targets for clean release metadata.

A clean full 40-hex revision produces the visible fork source URL
`https://github.com/ZelCore-io/blockbook/tree/<revision>` on every explorer page
and the status page's commit. Unknown, short, invalid or dirty metadata uses the
repository root labelled “Source (development build)”. The status commit also
labels development builds. Dirty identifiers never imply an exact source match.

## Corresponding source and qualification

Before public service, publish the matching source commit and required build
scripts and corresponding source in `https://github.com/ZelCore-io/blockbook`.
Verify its exact tree URL works without authentication and matches binary
metadata. Retain COPYING, notices and attribution in distributed artifacts.
A local unpublished development build does not establish AGPL compliance.

Run local unit checks with the pinned native toolchain, for example:

```sh
contrib/tests/run-unit-tests.sh
bash -n build/docker/deb/build-deb.sh
make -n build GITCOMMIT="$(git rev-parse HEAD)"
make -n deb-blockbook-bgold GITCOMMIT="$(git rev-parse HEAD)"
```

A native toolchain supplying RocksDB linker flags can require
`grocksdb_no_link`; equivalent affected-package checks use
`go test -tags unittest,grocksdb_no_link -count=1 ./server ./api ./db`.
Render tests exercise real production minification and debug templates,
exact/fallback source links and running-binary revision plumbing. Inspect
status, transaction and address output at desktop/mobile widths, including
navigation, currency controls, search, send transaction, attribution, terms,
ecosystem links and always-visible source. Keep all scripts and assets local.

Linux Docker/Debian packaging, exact published-source availability, real-wallet
and backend/API acceptance, recovery, and deployment remain separate operator
qualification. Schema numbers and fixtures do not prove database compatibility.
The current-master rollout uses a fresh index on one replica at a time, subject
to capacity and acceptance checks; retained deployed binaries and their database
snapshots provide rollback. No database, daemon, Ansible or deployment operation
is performed by the branding change. See the approved fork release management
design for the rolling fresh-index strategy and source/publication gates.
