# Native Insight compatibility profile

The optional native Go listener reuses Blockbook's query worker, mempool, transaction cache and
single database owner. It opens no second RocksDB index and makes no calls to the public HTTP
API. The V1/V2 APIs and explorer keep their existing bindings and behavior. This profile follows
the five route families of Zelcore's MIT-licensed `insight-to-blockbook` adapter; its notice is
retained in [COPYING.insight-to-blockbook](../COPYING.insight-to-blockbook).

Every response, including errors and OPTIONS, includes an HTTP `Link` header with `rel="source"`.
`Access-Control-Expose-Headers: Link` allows browser API clients to read it across origins.
Clean release metadata links to `https://github.com/ZelCore-io/blockbook/tree/<full-commit-sha>`;
unknown or dirty development metadata links to the repository with title `Source (development build)`.
An API-only deployment has the same prerequisite as the explorer: publish the clean matching source
revision and required build material before making the listener public. The development fallback
does not claim that unpublished local changes satisfy corresponding-source obligations.

Insight is disabled unless `-insight=host:port` is supplied. It accepts only Bitcoin-type chains
with eight decimal places, including BTG and GRS; startup fails for unsupported parsers or a port
collision. No production coin definitions enable it in this patch.

An operator may pair these optional coin-specification fields:

```json
{
  "ports": { "blockbook_insight": 9876 },
  "blockbook": { "insight_binding_template": "127.0.0.1:{{.Ports.BlockbookInsight}}" }
}
```

Merge these fields into a complete coin definition; the example is not a standalone definition.
The generated service includes `-insight=127.0.0.1:9876`. Both fields must be present and the
rendered numeric port must match. Port 9876 is an example requiring allocation, not an assumed
free port. An explicit bind host is mandatory. Loopback works only with a proxy on the same host;
for a proxy on another host, choose a reachable private interface and restrict access to the
trusted proxy. The listener serves plain HTTP; public TLS and perimeter request policy belong
at the existing proxy. This change makes no host or proxy configuration changes.

## HTTP contract

| Method and path | Response |
| --- | --- |
| `GET /api/status` | `{info:{blocks,version,protocolversion,difficulty,errors}}`; numeric backend fields, empty `errors` on success |
| `GET /api/addr/:address/utxo` | Array of address, txid, vout, amount, satoshis, height, confirmations and coinbase when known |
| `GET /api/addr/:address[/:noTxList]` | Balance summary with explicit zeros, string `balanceSat`, numeric other satoshi fields, `txApperances` and `unconfirmedTxApperances`; no transaction list |
| `GET /api/addrs/:address/txs?from=0&to=50` | `{totalItems,from,to,items}`; exclusive-end offsets |
| `POST /api/tx/send` | JSON `{rawtx:"..."}` to `{txid:"..."}` |

For adapter compatibility, address can instead be supplied by `?address=...` on `/api/addr`,
`/api/addr/utxo` and `/api/addrs/txs`. History also accepts `/api/addrs/:address/txs/:from[/:to]`;
path offsets take precedence over query offsets. `noTxList` is accepted and ignored.

History supports one address with integer `0 <= from <= to <= 1000`; defaults are 0 and 50.
Multiple addresses and larger windows receive 400. It requests page one of a bounded txid
prefix, includes pending transactions in worker order, deduplicates preserving first occurrence,
slices offsets, and fetches full transactions only for returned IDs. An offset beyond available
history returns an empty `items` array; actual `to` is `from + len(items)`. `totalItems` is the
worker's confirmed plus unconfirmed transaction counters. Counters and rows are not an atomic
chain snapshot and can drift during confirmation or reorgs. Overlapping client windows can
therefore change while the chain changes.

The prefix bounds the confirmed-history request and this handler's full-transaction enrichment.
The legacy worker still scans all pending address transactions, and the UTXO method returns the
full set for an address. These backend-work costs remain even for a small requested window.

All monetary JSON tokens are formatted from integer satoshis with eight-decimal precision and
no floating-point amount arithmetic. Numeric spending fields beyond JavaScript's safe integer
range (`-9007199254740991..9007199254740991`) fail explicitly with a generic 500 response.
Nil amounts become zero. History keeps txid, time, confirmations, fees, vin addr/value/valueSat
and coinbase, vout n/value/scriptPubKey addresses/hex/asm, blockheight/blockhash and transaction
hex where available. Missing input addresses are null. Output ASM is derived from script bytes
when present, including OP_RETURN; displayed address text is never converted into script ASM.

Broadcast requires a nonempty, even-length hexadecimal string and a JSON body at most 8 MiB,
including chunked bodies. Malformed, trailing or non-string JSON input receives 400; oversized
bodies receive 413. The daemon decides transaction acceptance. Rejection and unexpected RPC
errors use a generic 500 response; raw daemon messages, URLs, credentials and stack traces are
never returned. A backend error reported inside system-info with no Go error still receives a
generic 503. Error JSON has shape `{status:"error",error:"..."}`.

Known paths enforce GET or POST (405 otherwise), allow OPTIONS preflight (204), and set public
CORS headers on every response. Unknown routes receive 404. This is a compatibility profile,
not full Insight: Socket.IO, events, multi-address aggregation, history beyond the bound,
block routes and special/shielded asset semantics need separate contract work before migration.

## Readiness and shutdown

The bind occurs synchronously before startup succeeds. All five routes return 503 until an owned
atomic readiness flag is set. With `-sync`, this follows initial index sync, mempool initialization
and its first resync. Known-route OPTIONS remains 204 and method validation remains 405.
Without `-sync`, readiness follows the existing full public API startup point; it does not
certify a current index. Status is also gated because the inherited system-info worker
reads startup state that is changed during synchronization.

At most 32 requests enter handlers concurrently. Excess work receives immediate 503 with
`Retry-After: 1`; there is no waiting queue. HTTP limits are five seconds for headers, 30 seconds
for reading, 60 seconds for writing and idle connections, and 1 MiB of headers.

Every main return after startup runs registered cleanup before the deferred database close.
Normal signal shutdown drains Insight before closing the daemon. Admission closes first and
all active calls, including status, are tracked. If HTTP shutdown's deadline expires, connections
close but cleanup still waits for actual handlers before allowing database or daemon closure.
Legacy worker and broadcast methods cannot cancel, so a stalled backend call can extend process
shutdown beyond the HTTP deadline. The service manager's existing stop timeout is a separate
operator consideration.

## Recorded local verification

This is the current-master forward port of reviewed Insight commit
`dda65ea2ee6ddce23966c2804483b6337f3ad771`, on reviewed branding/policy base
`3624fa8e4230193599be56d532350112fb8b9fda`, including upstream
`b427738f`. Root/server/build-tool checks passed normally and with focused
Insight/lifecycle race detection on native macOS arm64 using Go 1.25.4,
RocksDB 9.10 and ZeroMQ 4.3.5. The offline full unit runner also passed,
and the native binary built and printed help without starting a daemon.
These checks do not exercise BTG/GRS daemons or qualify Linux packaging.

Current master's signal fan-out and explicit shutdown signal channel are retained,
along with synchronized initial-sync access, sync metrics, and shutdown joins for
state storage, index, mempool and internal-data healing. The broadcast closure passes
`false` for the current daemon API's alternative-RPC flag, matching the public API.
Configuration tests render actual service output in both dev and prod, preserving
optional runtime environment loading, jemalloc settings, backend dependency gating
and dev-only pprof. No production coin definition enables Insight.

Race checks use a fake query worker. They establish the exercised listener and
lifecycle behavior, not race freedom of every inherited worker path. Current
`api/worker.go` system-info still reads historical fiat timestamps directly while
`fiat/fiat_rates.go` updates them; startup gating does not establish safety for
those inherited concurrent accesses. Qualify a real worker with fiat updates
separately. No worker/fiat synchronization change is included here.

## Qualification before production use

Local fake-worker and template tests cover the contract and lifecycle. Linux packaging
and jemalloc runtime linkage remain unverified because Docker is unavailable. They do not
qualify an existing production database, the BTG/GRS daemons, a public proxy or real wallet
clients. Before a user-run canary:

1. Build from a clean pinned source revision with matching native libraries; publish that source
   and build instructions with the service as described in [the release guide](zelcore-release.md).
2. Follow the approved current-master fresh-index plan: drain one replica at a time,
   build its index afresh, and verify height/hash, API/wallet behavior and resources
   before switching traffic and advancing to the next replica. First confirm replica
   availability and disk capacity; a single replica needs temporary capacity or a
   planned outage. Retain deployed binaries and their matching database snapshots
   for rollback. Matching schema 7 is not compatibility proof. Never point two writers
   at one live database.
3. Exercise daemon broadcast rejection/acceptance, coinbase and OP_RETURN transactions, pending
   balance/history changes, BTG/GRS address handling and wallet display/export/spending contracts.
4. Allocate a listener port, choose the private bind interface, configure the trusted proxy and
   verify TLS, CORS, 503 readiness, saturation, body limits and shutdown under active requests.
5. Retain a recoverable snapshot and agree on canary/rollback steps. Matching schema and unit
   fixtures alone do not prove production database compatibility. No schema migration is added
   here; rollback across any other write migration requires restoring its pre-migration snapshot.

This work performs no deployment, daemon integration, production snapshot rehearsal or source
publication. No production connectivity is required for the local test suite.
