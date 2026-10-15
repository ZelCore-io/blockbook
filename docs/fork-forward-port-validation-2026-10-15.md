# Forward-port validation — 2026-10-15

This records local qualification of the current-upstream branding and optional native
Insight stack. It is a development candidate, with no release tag or production
qualification. Results below identify the source actually tested; this documentation
commit does not change executable source. The [release policy](fork-release-policy.md),
[build/source guide](zelcore-release.md) and [Insight contract](insight-compatibility.md)
remain the operating references. Compact reproduction material is retained in
[validation/2026-10-15-forward-port](validation/2026-10-15-forward-port/README.md).

## Source and review map

| Branch or reference | Exact revision | Scope |
| --- | --- | --- |
| Local `master` | `44242e0d24d1b9a78ce77df05c9221f6789ba463` | Original maintained baseline, unchanged |
| `chore/fork-release-policy` | `e9076e26a0305cb3e22615ef9f17b0c9a6f95a88` | Approved policy, draft-safe CI and proposed protections |
| Pinned upstream | `b427738facd3a8c4cf3986e6e11d868a214deeea` | Fresh baseline reviewed before feature ports |
| `sync/upstream-20261015` | `cea776de9431de0f0f427b652f3bd340ad653184` | Merge of policy first parent and pinned upstream second parent |
| `feature/zelcore-branding` | `3624fa8e4230193599be56d532350112fb8b9fda` | Branding, clean/full source metadata and build guards |
| `feature/native-insight` executable-source candidate | `147edd6170c323813cd50fbde6a4a37ecd9000ee` | Optional listener, lifecycle and configuration integration |
| `release/zelcore-0.5` | `c897036701cd7ac1a9b84ea7d83534114ec6c8a9` | Temporary maintenance candidate from old branding base |

Original source branches remain untouched: `codex/blockbook-branding-0.5` at
`a3bcc18a774d0122ff15832ee79763b34973a406` and
`codex/blockbook-insight-0.5` at `7a80e2bdb3a55e58390996cc4ffc20912e715977` (the reviewed Insight implementation
is `dda65ea2ee6ddce23966c2804483b6337f3ad771`). Their older qualification is not
current-source or rollback qualification. Existing PR #1 and unrelated
`add-clore`, `add-tls` and `zelcore` branches are preserved.

The four upstream commits after `44242e0d` were reviewed in full:

| Commit | Reviewed change |
| --- | --- |
| `8fce27e1cae1e2f8f416c0a4b92b4c2585c4154d` | Node-reported Ethereum/Tron header hashes and subscription types |
| `72f7947658cf535d91d1021f07ea3710ef0fadf4` | Skip RPC-dependent tests for Dependabot |
| `1684a2450589bc5944ef63f5f0a8b8b26e1d3a99` | Go GC/live-heap metrics and Grafana panels |
| `b427738facd3a8c4cf3986e6e11d868a214deeea` | Retain EVM token transfers when contract lookup fails |

The sync merge resolved only the workflow conflict: draft/same-repository guards
remain on all self-hosted jobs, with upstream's Dependabot exclusion retained on
connectivity/integration and manual dispatch retained. Its tree differs from upstream
only in the six reviewed policy/workflow/design/plan paths. Independent spec and code
quality reviews accepted Tasks 1–4 with no Critical or Important findings. Final
combined read-only review is a controller handoff after this record is prepared.

## Local evidence and results

All native results used Darwin arm64, Go **1.25.4**, RocksDB **9.10.0** and ZeroMQ
**4.3.5**, offline module settings and the supported `grocksdb_no_link` tag.
The installed system Go was 1.26.5; the scratch environment selected the downloaded
1.25.4 binary directly with `GOTOOLCHAIN=local`. Bash 5.3 supplied `mapfile` for
the unchanged upstream unit wrapper; macOS's Bash 3 is insufficient.

| Source/check | Observed result |
| --- | --- |
| Original `44242e0d` archive | Full unit wrapper: 50 passing packages, 15 without tests; native build; common and narrow server race smoke passed; 901 regular source files unchanged |
| Pinned `b427738f` archive | Full unit wrapper: 50 passing packages, 15 without tests; native build; focused common metrics/EVM header race tests passed; 903 regular source files unchanged |
| Policy/sync validation | 72 workflow guard evaluations passed at sync; 22 Grafana tests and render check (122 panels, 105 metrics); 69 pipeline fixture tests; artifact validator zero errors, two existing checksum-verified HTTP warnings for nuls/unobtanium |
| Maintenance `c8970367` | 100 configuration JSON files parsed; Debian helper shell syntax and committed whitespace checks passed; workflow/rules structural checks passed |
| Branding `3624fa8e` | Server/API/DB normal checks, source render/helper checks with default/clean/dirty metadata, native build with exact committed SHA, release guard positive/negative fixture checks and build dry runs passed |
| Combined native `147edd61` | Full offline wrapper: **51 passing packages**; focused root/server/build-tools normal and Insight/lifecycle race checks passed; exact-SHA native build, help-only execution and source-header/helper tests passed |

The combined race selector was
`TestSignalShutdownDrainsInsightBeforeDaemon|TestInsight` on `. ./server ./build/tools`.
It uses fake workers: it establishes exercised listener/lifecycle behavior, not race
freedom of all inherited query paths. Current `api/worker.go` directly reads historical
fiat timestamps while `fiat/fiat_rates.go` updates them. Real-worker qualification with
fiat updates remains outstanding. Pending history still scans all pending address
transactions; UTXO work is not bounded by the history window. Uncancelable worker or
broadcast calls may extend shutdown beyond the HTTP deadline.

The actual current daemon signature is `SendRawTransaction(string, bool)`; the narrow
Insight wrapper passes `false`, matching the public API. Compilation exposed the old
single-argument assumption, and a retained-command negative control reproduced that
compile failure. Lifecycle and service-generation red controls failed before their
implementation; route readiness, exact money, broadcast rejection, source/CORS,
admission and shutdown checks then passed. Both dev and prod service output preserve
existing environment/jemalloc/backend/pprof behavior. No production coin enables Insight.

Feature invariance compares against **b427738f**, not the old 0.5 database baseline:
`db`, `bchain`, `go.mod`, `go.sum` and production `configs/coins` are unchanged by
branding/Insight. Reviewed Insight implementation, money types and MIT notice match
`dda65ea2` byte-for-byte. These facts do not imply old-index compatibility.

Controller browser inspection used actual fixture-backed rendered HTML over loopback:
status at 1365×900 desktop and 390×844 mobile; mobile production transaction, debug
address and dirty debug status. DOM and inline screenshots were inspected; no screenshot
files were saved. All seven footer links stayed within bounds (mobile footer 94.375px).
The existing status 400px scroll width at a 390px viewport remains. Preview clean metadata
was injected `cea776de` before the branding commit; later committed-SHA render tests
passed separately. Dirty metadata used the development root. No external link was clicked;
the owned preview page/server were closed.

## Scope of native qualification

RocksDB was a portable static build with system zlib/bzip2 and Homebrew LZ4 1.10.0/
Zstd 1.5.7; Snappy, GFlags and Jemalloc were disabled. ZeroMQ curve/libsodium/drafts
were disabled. Existing duplicate-library warnings and macOS race linker
`LC_DYSYMTAB` warnings occurred; recorded green commands exited zero.
The Docker client existed but its desktop-linux daemon socket was absent. No Linux
Docker/Debian package, static jemalloc runtime or production allocator qualification
is claimed. Apt/image inputs are not fully reproducible.

No daemon startup (binary execution was help only), integration/backend connectivity,
real-wallet test, production database/snapshot access, fresh sync, canary, Ansible, SSH,
host action or deployment occurred. Matching schema 7 is insufficient proof of database
compatibility. Original-baseline tests and old source-branch evidence remain distinct.

## Future rolling fresh-index qualification

Use a fresh **Blockbook index**, one server at a time. Preserve daemon/chain data where
it contains the required history; pin the daemon version independently from Blockbook,
configuration and artifact. Before choosing each coin, record parser/config support and
RPC compatibility on this candidate. Existing PR #1's Clore/Telestai (TLS) changes are not merged
by this stack; do not infer their coin adapters or configuration are available here.

Preflight must verify historical blocks required for a full index are available. A
pruned daemon may need an approved historical-block source or daemon resync before the
Blockbook rebuild. Confirm serving replica capacity, initial-sync duration and disk
capacity for the **new index plus retained old database snapshot**, other data and
operating headroom. A single-replica coin needs temporary capacity or a planned outage;
this record makes no zero-downtime promise.

Drain one eligible replica, build into a fresh directory, check full height/hash sync,
mempool, daemon RPC, API/Insight/proxy behavior, real wallet display/export/spending and
resource headroom, then accept and observe it before traffic cutover and the next server.
Never run two writers on one live index. Retain the actual deployed artifact and its
compatible old database snapshot and rehearse recovery. The unqualified new 0.5 branch
is a maintenance reference, not the rollback artifact. Copied-database compatibility
checks are additionally required if old-index reuse is proposed.

## Publication and protections handoff

At preparation of this record no branches/PRs/rulesets were published or changed by
this task. Historical read-only observation found no rulesets/master protection; the
controller must reread current and inherited settings before additive application.
Static guards/schema checks are not hosted Actions evidence.

After final review, the authorized controller owns publication of maintenance and
original stacked source branches plus four **draft** PRs: policy→master,
sync→policy, branding→sync, Insight→branding. Describe each base scope accurately;
as earlier PRs merge, retarget descendants in order and revalidate their diffs/checks.
Preserve PR #1. No master merge, release tag or deployment is part of this handoff.

Verify unauthenticated exact-tree source URLs against artifact full-SHA metadata before
public service. Read actual hosted lint outcomes and skipped self-hosted jobs, without
marking skipped jobs as executed tests. Apply reviewed additive protections and read
back exact refs, conditions and rules; do not fabricate required status contexts.
Converting a draft to ready enables eligible self-hosted jobs; converting back does not
cancel an already running job. Record actual PR URLs, runs and protection readback in
the controller's publication report; they are not asserted here in advance.
