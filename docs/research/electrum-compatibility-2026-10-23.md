# Electrum compatibility feasibility: GRS first

Date: 2026-10-23. Blockbook baseline: `c5ade771de895035480adf0f411e6250ff243183`.
Wallet source inspected: ZelTreZ `8b90f535e2e851d243a7e1dedf103c8738fa9625`.

## Decision

A native, optional Go Electrum listener in the existing Blockbook process is
plausible. It can share the already-open RocksDB handle, chain adapter, parser,
and mempool; a second full indexer process is unnecessary. It is **not yet a
thin protocol adapter**: exact script identity, historical backfill, coherent
mempool views, and removal/reorg notifications need additional work.

Proceed with an isolated GRS proof of concept after agreeing its protocol
version and fixtures. GRS is the requested first test, although its current
ZelCore path already uses Blockbook. Do not enable another coin by inheritance.
This study adds documentation only: no listener, database migration, deployment,
network broadcast, or runtime behavior change; no interoperability or performance
result is claimed.

## What clients actually require

ZelTreZ `src/renderer/store/modules/balances/electrum/actions.js` calls HTTP
`balance&param=<address>`; transactions use `nicehistory`; sending uses `niceutxo`
and `broadcast`; `utils/sendingHelpers/logicParts/utxo.js` also uses `utxo`.
These are proxy contracts, not Electrum wire methods. The proxy implementation
and its negotiation are not present in the inspected paths. Native Electrum
support alone does not replace it. GRS in `storage/coinsSimple.js` has
`type: "blockbook"`, despite retaining an `electrum` metadata field.

For a direct SPV client, the candidate method inventory is below. It is a work
list, not evidence of a complete version implementation. Pin the actual GRS
wallet release and capture a read-only handshake/request trace before coding.

| Need | Wire methods | Existing building blocks / missing work |
| --- | --- | --- |
| Negotiate / identify | `server.version`, `server.features`, `server.banner`, `server.ping` | New dispatcher, network/genesis identity and honest version range |
| Discover peers | `server.peers.subscribe`, optional `server.add_peer` | No discovery implementation; decide applicable version requirements |
| Sync headers | `blockchain.headers.subscribe`, `blockchain.block.header`, `blockchain.block.headers` | Height/hash lookup exists; raw serialization/cache and checkpoint proofs do not |
| Discover funds | `blockchain.scripthash.get_history`, `.get_balance`, `.listunspent`, `.get_mempool` | Descriptor history/balances plus mempool exist; exact script index and coherent conversion needed |
| Track funds | `blockchain.scripthash.subscribe`, `.unsubscribe` where applicable | New status computation and lifecycle invalidation |
| Fetch / verify tx | `blockchain.transaction.get`, `.get_merkle`, `.id_from_pos` where applicable | Raw transactions/block txids available through adapters; branch builder and canonicality checks needed |
| Fees | `blockchain.estimatefee`, `blockchain.relayfee`, `mempool.get_fee_histogram` where applicable | Fee estimates exist; units, unavailable result, relay policy/histogram need mapping |
| Send | `blockchain.transaction.broadcast` | Existing `SendRawTransaction`; implementation must preserve backend validation/errors; no broadcast in study |

The [protocol basics](https://electrum-protocol.readthedocs.io/en/latest/protocol-basics.html)
define framing, negotiation, SHA256 of exact locking-script bytes with reversed
hex byte order, and history-derived subscription status. The
[method reference](https://electrum-protocol.readthedocs.io/en/latest/protocol-methods.html)
defines individual schemas. These are living 1.7.x documents: do not copy the
latest schemas into an older negotiated version. For example, header chunks
changed representation in 1.6. Advertising a version requires its mandatory
methods, not just the wallet's observed subset. A private incomplete POC must
be labelled as such and cannot advertise general compatibility.

## Existing storage and exact-script gap

Local evidence:

- `db/rocksdb.go`: base column families are `default`, `height`, `addresses`,
  `blockTxs`, `transactions`, `fiatRates`; Bitcoin-family additions are
  `addressBalance`, `txAddresses`, `blockFilter`. There is no Electrum script-hash
  reverse lookup. `GetAddrDescTransactions` scans descriptor/height keys newest
  first. `GetAddrDescBalance` exposes confirmed balances/UTXOs;
  `GetTxAddresses` exposes indexed input/output descriptors and amounts.
- `bchain/coins/btc/bitcoinlikeparser.go:GetAddrDescFromVout` converts P2PK into
  P2PKH, deliberately combining their address history. `GetScriptFromAddrDesc`
  returns the descriptor, which cannot undo that conversion.
  `IsAddrDescIndexable` excludes empty and OP_RETURN scripts.
- `bchain/coins/grs/grsparser.go` embeds the Bitcoin parser. Its checksum and
  address/network parameters are coin-specific; descriptor normalization still
  applies.

A 32-byte hash cannot be inverted into the existing variable-length descriptor.
A lookup `scriptHash -> descriptor` works only where descriptor equals the
original script and the index preserves the intended history. Hashing normalized
P2PKH descriptors would lose P2PK identities, and mapping both hashes to the same
history would leak unrelated transactions into each result. Similarly, a zero
answer for every omitted script would conceal unsupported history.

Recommended complete design: preserve original script bytes at output ingest;
add script-hash keyed history and UTXO/balance data (or a script-identity indirection
with equivalent exact filtering), and an outpoint-to-exact-script association
for spends. Deduplicate a tx touching the same script multiple times. Keep the
existing normalized index unchanged for existing consumers. Decide/document the
reference server's unspendable/genesis exclusion policy using differential
fixtures, rather than assuming every omitted script should be inserted.

A smaller exploratory implementation can support a declared set of standard
scripts and use descriptor lookup for them, but P2PK/P2PKH contamination must still
be filtered using original outputs and previous outputs. This can be expensive
and cannot justify an unrestricted compatibility claim. Unknown valid hashes
should produce protocol empty/null results only once complete coverage is known;
partial coverage is a readiness failure, not an empty wallet.

### Backfill and operational cost

Scanning distinct existing descriptors is O(D) and can populate a standard-script
reverse lookup; it cannot reconstruct normalized-away P2PK data or omitted
scripts. `transactions` is a transaction cache, not demonstrated complete archival
raw-block storage. A correct complete backfill therefore needs historical raw
transactions/blocks from an archival backend or a fresh replay. Pruned backend
availability is a gating question. Processing is at least O(outputs + inputs)
with previous-output resolution and extra write amplification/compaction.

Let D be distinct exact scripts, L their mean length, H script/transaction
associations, U unspent outputs. A reverse map alone has a raw payload floor near
D*(32+L) bytes, before key framing, RocksDB indexes, compression and compaction.
Exact histories add O(H), UTXOs O(U), and raw fixed GRS headers roughly 80*(tip+1)
bytes. These are sizing formulas, **not measured disk or runtime estimates**.
Measure D/H/U, replay throughput, RPC bytes, peak temporary disk, cache pressure,
and compaction on a copy before quoting hours or capacity.

Add versioned readiness metadata and a resumable height/hash checkpoint. Backfill
into staging families with a canonical tip fence; catch up deltas and rollback
on a changed checkpoint, then atomically mark coverage ready. Connect/disconnect
must update the new data in the same logical commit as existing indexes, with
undo data inside the configured reorg retention. Never serve incomplete coverage.
Restart must distinguish absent, in-progress, ready and incompatible schema.

## Optional native listener design

Construct an `ElectrumServer` alongside the public server in `blockbook.go`,
injecting the same `*db.RocksDB`, `bchain.BlockChain`, parser and mempool. Default
off, explicit bind/network and TLS policy; do not open the same database from a
second writer or proxy queries through the public HTTP API. Restrict initial
capability to GRS and the selected network. Shutdown must cancel queries,
drain bounded queues and close connections before shared resources close.

Separate JSON-RPC framing/validation, negotiated method schemas, chain-specific
header/merkle codec, and a query service returning one canonical generation.
Use immutable cache entries keyed by block hash and bounded request deduplication.
The current DB API does not promise an atomic DB-plus-mempool snapshot: add a
read view or generation fence, retry boundedly on a tip/mempool-generation change,
and fail explicitly when consistency cannot be obtained. A snapshot of RocksDB
alone cannot freeze mutable mempool state.

`bchain/types.go` exposes `GetBlockHash`, `GetBlockInfo`, `GetBlockRaw`,
`GetTransaction`, `EstimateFee`, `EstimateSmartFee`, and `SendRawTransaction`.
`GetBlockHeader` returns the limited `BlockHeader` struct, not raw header bytes;
its Bitcoin RPC implementation requests verbose output. `BlockInfo` has the
merkle root and ordered txids; serialized headers can come from raw blocks or a
new raw-header adapter/cache. No shared header-merkle checkpoint index or ready
Electrum transaction proof API was found. Prefer a narrow raw-header adapter
over downloading full blocks for every header query.

## GRS headers, hashes and proofs

GRS needs its own tested codec. Its upstream
[header declaration](https://github.com/Groestlcoin/groestlcoin/blob/master/src/primitives/block.h)
serializes the usual six fields (80 bytes). Its
[header hash implementation](https://github.com/Groestlcoin/groestlcoin/blob/master/src/primitives/block.cpp)
uses the Groestl writer. Do not validate a GRS header with Bitcoin SHA256d.
The Blockbook GRS parser configures Groestl address checksums and `grs`/`tgrs`/
`grsrt` witness prefixes; these do not change Electrum's script-hash function.

The upstream [transaction implementation](https://github.com/Groestlcoin/groestlcoin/blob/master/src/primitives/transaction.cpp)
distinguishes txid serialization without witness from witness hashes. Its
[merkle implementation](https://github.com/Groestlcoin/groestlcoin/blob/master/src/consensus/merkle.cpp)
builds the transaction root from txids, duplicates odd final leaves and uses
SHA256d pair hashing. Confirm deployed-node version with fixture vectors before
shipping; upstream master is evidence for design, not proof of deployed behavior.
The [GRS ElectrumX coin implementation](https://github.com/Groestlcoin/electrumx/blob/master/electrumx/lib/coins.py)
also overrides header hashing and address checksums and selects a GRS deserializer.

For `get_merkle`, locate tx in the canonical ordered block txid list, construct
siblings in internal byte order, return protocol display order/position/height,
and recompute the root against the raw header. Do not use wtxids. A backend
`GetBlock` result that skips unavailable transactions (see `grsrpc.go`) is not a
safe proof input; use the complete ordered txid list and fail on inconsistency.
Recheck canonical block hash after a backend read; invalidate height-keyed caches
on forks. Header checkpoint proofs are a different merkle tree and require
separate storage/computation; transaction branches do not implement them.

## Mempool, forks, subscriptions and limits

`bchain/types.go:Mempool` provides descriptor transaction queries and all entries.
`MempoolBitcoinType.Resync` removes absent entries; `OnNewTx` is invoked for new
tx payloads, with no corresponding removal callback in that interface.
`blockbook.go` fans out new-block/new-tx callbacks. `db/sync.go` disconnects a fork
then reconnects; the callback type does not communicate disconnected scripts.
These are useful hooks, not a complete Electrum invalidation feed.

Add post-commit change events covering both funding and spent scripts, removed
mempool txs and descendants, replacements, confirmation transitions, disconnected
blocks, and rollback-only tip changes. Reorg handling must invalidate scripts
touched only by the removed branch, not just the replacement blocks. Compute
statuses from the same ordered history returned by queries. Register subscription
and read its initial status under a generation fence to avoid lost updates.
Coalesce dirty scripts, recompute, send only changed statuses, and release all
references on unsubscribe/disconnect. Never block the sync writer on socket I/O.

The overlay must remove confirmed UTXOs spent by mempool transactions, include
unspent mempool outputs, handle dependent chains, and compute net unconfirmed
balance without double counting. Distinguish unconfirmed transactions whose
parents are confirmed (height 0) from those with unconfirmed parents (height -1),
include fee fields where required, and preserve stable history/status ordering.
An empty script has null status. Confirmed history needs increasing height and
actual block position; simply reversing the current DB iterator is insufficient
without verifying same-block ordering. Coinbase maturity/UTXO presentation must
match the selected coin wallet and reference server.

Before any public listener, configure caps for connections per IP/global,
subscriptions per session/global, request bytes/depth, batch size, concurrent
queries, history/response bytes, header count, backend calls, cache memory and
queued notifications. Use read/write/idle/query deadlines and cancellation,
per-client work accounting and bounded worker pools. Large histories must produce
an explicit protocol error under the chosen policy, never silent truncation that
looks like a complete history. Slow clients disconnect when queues fill.
Measure sync lag, DB latency and existing websocket behavior under shared load;
Blockbook already targets at least 20,000 websocket connections (AGENTS.md).
Metrics should avoid raw addresses/scripts/client identifiers as labels.

## Smallest useful GRS proof of concept and acceptance gates

1. Pin a released GRS wallet, reference Electrum server and daemon version,
   protocol range and network. Use disposable local GRS regtest where supported;
   confirm those versions' regtest support first. Otherwise use offline fixtures
   plus read-only public-chain queries. No production wallet, broadcast or deployment.
2. Build offline codec/query tests before a loopback listener: unknown hash,
   P2PKH, P2SH, witness v0/v1 where supported, P2PK and matching P2PKH as distinct
   scripts, multiple touches/same-block spends, empty/OP_RETURN policy, coinbase,
   non-witness/witness txids, odd-leaf merkle paths, genesis and non-genesis headers.
   Compare byte-for-byte against pinned node/reference outputs. Check all roots.
3. Replay a bounded disposable chain slice into a separate test database with
   original-script identity retained. Compare exact history/order, integer atomic
   balances, UTXOs, fees and status hashes. Record coverage bounds; historical
   partial replay is not a complete wallet server.
4. Expose only a private loopback experimental session with handshake, headers,
   script history/balance/UTXO/subscription and transaction/proof methods needed
   by the pinned client. Stubbed methods must be explicit errors; no general
   protocol-version claim until its full mandatory inventory passes.
5. Drive deterministic mempool add/spend/parent-child/removal/replacement,
   confirm, two-branch reorg including scripts only on the orphaned branch,
   restart during backfill, and subscribe-during-transition cases. Use simulated
   events/offline fixtures first; any later local node mutations require their
   own implementation authorization. Assert final statuses equal fresh reads,
   no stale proof/header result, no lost update, and clean disconnect cleanup.
6. Measure baseline versus POC on the same machine/data: request p50/p95/p99,
   CPU/RSS, DB growth/compaction, RPC rate, sync throughput and lag. Exercise
   oversized requests/batches, large histories, slow peers and caps. Agree numeric
   performance budgets before the run; none have been measured by this study.
7. Exit only with reproducible fixtures/results, complete selected-version
   conformance, no existing API regression and a measured backfill/rollback plan.
   A full production proposal follows separately; stop if exact script history
   cannot be recovered from available archival data.

## Known, uncertain, and follow-up

**Known from source:** shared in-process dependencies are available; current
indexes are descriptor-oriented and normalize P2PK; raw headers/proofs and full
Electrum lifecycle hooks need work; ZelCore's current GRS route is Blockbook.

**Not established:** deployed GRS daemon/archive retention, target wallet/proxy
wire inventory, reference exclusion policies, complete historical raw-data
availability, exact storage footprint/backfill duration, live load behavior,
and interoperability. No local database or node was opened to fill these gaps.

Recommendation: authorize a separate bounded GRS prototype once client/version
and data availability are pinned. Keep the feature disabled by default, preserve
existing consumers, and budget for exact script indexing rather than promising
a listener-only integration. This PR is a feasibility decision record.

## Validation of this documentation change

`git diff --check` passed. Both required repository wrappers were attempted:
`contrib/tests/run-unit-tests.sh` and
`contrib/tests/run-integration-tests.sh -run 'TestIntegration/.*/connectivity'`.
Both stopped in the system prerequisite check because `CGO_CFLAGS` and
`CGO_LDFLAGS` were not exported. No unit/connectivity test ran or passed; no node
reachability or runtime compatibility is inferred from this documentation review.
