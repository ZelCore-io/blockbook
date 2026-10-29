# Native Insight compatibility profile

The optional native Go listener reuses Blockbook's query worker, mempool, transaction cache and
single database owner. It opens no second RocksDB index and makes no calls to the public HTTP
API. The V1/V2 APIs and explorer keep their existing bindings and behavior. This profile
serves every bitcore Insight route Zelcore's wallets use. Each response is a superset of what Zelcore's MIT-licensed `insight-to-blockbook` adapter (the converter) returns today: no
converter path, field name or value changes, and stock Insight fields are added alongside. Its
notice is retained in [COPYING.insight-to-blockbook](../COPYING.insight-to-blockbook).

Every response, including errors and OPTIONS, includes an HTTP `Link` header with `rel="source"`.
`Access-Control-Expose-Headers: Link` allows browser API clients to read it across origins.
Clean release metadata links to `https://github.com/ZelCore-io/blockbook/tree/<full-commit-sha>`;
unknown or dirty development metadata links to the repository with title `Source (development build)`.
An API-only deployment has the same prerequisite as the explorer: publish the clean matching source
revision and required build material before making the listener public. The development fallback
does not claim that unpublished local changes satisfy corresponding-source obligations.

Insight is disabled unless `-insight=host:port` is supplied. It accepts only Bitcoin-type chains
with eight decimal places, including BTG and GRS; startup fails for unsupported parsers or a port
collision.

## Enabling it

A coin config opts in with a pair of fields, and both must be present (the rendered numeric port
must match `blockbook_insight`):

```json
{
  "ports": { "blockbook_insight": 9876 },
  "blockbook": { "insight_binding_template": "${BLOCKBOOK_INSIGHT_HOST}:{{.Ports.BlockbookInsight}}" }
}
```

`configs/coins/bitcoin.json` and `configs/coins/zcash.json` carry exactly this. The generated
service has `-insight=${BLOCKBOOK_INSIGHT_HOST}:9876` in `ExecStart`, and systemd expands the
variable from `EnvironmentFile=-/etc/blockbook/blockbook.env` when the service starts. The host
is a deploy-time value and is never committed: set `BLOCKBOOK_INSIGHT_HOST` to the host's private
`10.100.0.x` address in that file (see [env.md](env.md)). The template check accepts the exact
token `${BLOCKBOOK_INSIGHT_HOST}` as the host and nothing else containing `$`; any other
environment reference is rejected at build time. Other coins may use a literal host such as
`127.0.0.1:{{.Ports.BlockbookInsight}}`. An explicit bind host is mandatory: an empty host (the
variable unset) makes Blockbook refuse to start.

`contrib/scripts/deploy-blockbook.sh` therefore refuses an install of an Insight-enabled coin
before it builds or stops anything when the env file does not define `BLOCKBOOK_INSIGHT_HOST`
as a dotted IPv4 address other than `0.0.0.0`, when that address is not assigned to the host
(`ip -o -4 addr show`), or when the port is held by a process other than this coin's running
Blockbook (`ss -ltnp`). On the first BTC and ZEC switch the port is held by the converter, so
stop and disable it first; the refusal says so. It also refuses a package whose unit lacks the
flag (for example a stale `--skip-build` package). After the restart it probes
`http://<host>:<port>/api/status` (200, or 503 while not ready); a refused connection is a failed
check and rolls the install back. All of this holds with `--skip-build` and
`--use-package-unit`, which the production deploys use. Without `--use-package-unit`, a host
unit or `ExecStart` drop-in keeps its own command line and the listener is not started.
Coins without `ports.blockbook_insight` are not checked or changed.

On BTC and ZEC hosts the listener takes over port 9876 from the converter (the converter is
stopped and disabled, not removed), so HAProxy needs no change. Port 9876 is registered for both
coins in the "blockbook insight" column of [ports.md](ports.md); two coins may share it because
they run on separate hosts. The listener serves plain HTTP; public TLS and perimeter request
policy belong at the existing proxy, and the private interface should be reachable only by the
trusted proxy.

## HTTP contract

Every route is served under both `/api/` and `/insight-api/`. OPTIONS on any known path is 204,
a known path with the wrong method is 405 with `Allow`, and unknown paths are 404.

| Method and path | Response |
| --- | --- |
| `GET status` | `{info:{blocks,version,protocolversion,difficulty,errors}}`. `?q=getDifficulty` gives `{difficulty}`, `?q=getBestBlockHash` the backend best hash, `?q=getLastBlockHash` `{syncTipHash,lastblockhash}` from the index tip |
| `GET sync` | `{status,blockChainHeight,syncPercentage,height,error,type}`; `finished` only when Blockbook is in sync, `blockChainHeight` is the daemon height and `height` the index height |
| `GET peer` | `{connected:true,host:"127.0.0.1",port:null}` |
| `GET version` | `{version}` of this build |
| `GET blocks?blockDate=YYYY-MM-DD&limit=N&startTimestamp=T` | `{blocks,length,pagination}`; default and maximum `limit` 200 |
| `GET block/:hash` | Block with txids, reward (coinbase outputs minus block fees), confirmations and `isMainChain` |
| `GET rawblock/:hash` | `{rawblock}` hex |
| `GET block-index/:height` | `{blockHash}` |
| `GET tx/:txid` | Transaction in the superset shape below. `spentTxId`, `spentIndex` and `spentHeight` come from the extended index when the coin has one (BTC); without it (ZEC) `spent` is still true/false but `spentTxId` is null, because finding a spender would walk the whole history of each spent output's address |
| `GET rawtx/:txid` | `{rawtx}` hex |
| `GET txs?block=<hash>&pageNum=N` | `{pagesTotal,txs}`, 10 transactions per page |
| `GET txs?address=A&pageNum=N` | `{pagesTotal,txs}`, 10 per page, newest first with pending first |
| `POST tx/send` | JSON or form `{rawtx:"..."}` to `{txid:"..."}` |
| `GET addr/:address[/:noTxList]` | Balance summary with explicit zeros, string `balanceSat`, numeric other satoshi fields, `txApperances`, `unconfirmedTxApperances` and a `transactions` txid list (default window 0..1000) |
| `GET addr/:address/balance`, `totalReceived`, `totalSent`, `unconfirmedBalance` | Bare satoshi integer, as stock Insight. The converter answered the full address summary object on these paths; this is expected difference E11 in `contrib/insightcompare` |
| `GET addr/:address/utxo` | Array of address, txid, vout, amount, satoshis, height, confirmations and coinbase when known; the full set, not capped |
| `GET addrs/:address/txs?from=0&to=50` | `{totalItems,from,to,items}` for one address; exclusive-end offsets |
| `GET addrs/A,B/txs`, `POST addrs/txs` | Merged history of at most 20 addresses, same shape |
| `GET addrs/A,B/utxo`, `GET addrs/A,B/unspent`, `POST addrs/utxo` | Concatenated unspent rows of at most 100 addresses; `unspent` returns confirmed outputs only |
| `GET addrs/A,B/balance` | `{balance,unconfirmedBalance}` in satoshis, summed over at most 100 addresses |
| `GET utils/estimatefee?nbBlocks=2,3` | `{"2":<coins/kB>,...}`; `-1` when the daemon cannot estimate |
| `GET currency` | `{status:200,data:{rate:<USD>,short:"<TICKER>"}}`; rate 0 without a fiat source (Firo: stock shape, see [Firo extension](#firo-extension)) |
| `GET`/`POST messages/verify` | `{result:<bool>}` from the daemon's `verifymessage` |

For adapter compatibility, a single address can instead be supplied by `?address=...` on `addr`,
`addr/utxo` and `addrs/txs`. History also accepts `addrs/:address/txs/:from[/:to]` and
`addrs/txs/:from[/:to]?address=`; path offsets take precedence over query offsets. A single-address
route given a comma-separated value gets 400. POST bodies may be JSON or
`application/x-www-form-urlencoded`; both are treated the same.

### Transaction shape (superset rule)

`tx`, history items and `txs` items share one transform. Every field the converter returns keeps
its name and value, and stock Insight fields are added next to them (extra fields are allowed).
Top level: `txid`, `version`, `confirmations`, `locktime` and `lockTime`, `blockhash` and
`blockHash`, `blockheight` and `blockHeight` (-1 while pending), `blocktime`, `blockTime` and
`time`, `size`, `vsize`, `hex`, `value` and `valueIn` (satoshi strings), `valueOut` and `fees` (coins,
numbers; `fees` is 0 when unknown), `isCoinBase` (only for a coinbase) and optional
`confirmationETABlocks`, `confirmationETASeconds`, `rbf`, `coinSpecificData`, `chainExtraData`.
`vin` entries keep the converter keys (`txid`, `vout`, `sequence`, `n`, `addresses`, `isAddress`,
`value`, `valueSat`, `hex`, `coinbase`) and add `addr`, `scriptSig{hex,asm}`, `doubleSpentTxID`
and a `scriptPubKey` clone of the vin keys. `vout` entries keep `value`, `n`, `hex`, `addresses`
(OP_RETURN text included, null when absent), `isAddress`, `type`, spent fields, and add
`scriptPubKey{value,n,spent,hex,addresses,isAddress,type,asm}`. History items also set `isOwn` on
inputs and outputs that belong to the requested address, or to any requested address for the
merged routes.

**ASM decision.** `asm` is the disassembly of the script bytes, never address text. This matches
stock Insight and gives the wallet's OP_RETURN memo parser the same text as the converter for
ASCII memos and correct text for binary ones; the converter instead writes address text into
`asm` and double-encodes non-ASCII data. It is an expected difference from the converter.

### History windows and deep paging

For one address, `from` and `to` are integers with `0 <= from <= to` and `to - from <= 1000`.
There is no upper bound on `to` itself, so a wallet can page past 1000 transactions: the handler
reads the worker pages that cover the window (at most two), slices by offset in the worker's
sequence (pending first, then newest first), deduplicates preserving first occurrence, and fetches
full transactions only for the returned ids. The default window is 0..50 for `addrs`, 0..1000 for
`addr`; as in stock, `from` and `to` are used only when both are given. Anything malformed or
over the bound gets 400. An offset beyond the end returns an empty `items` array and does not scan
the history (one balance read); the actual `to` is `from + len(items)`. `totalItems` is the
worker's confirmed plus unconfirmed counters. Counters and rows are not an atomic chain snapshot
and can drift during confirmation or reorgs, and a duplicate that straddles two windows can
appear in both.

### Multi-address bounds

Duplicates and blanks collapse, preserving first occurrence. At most 20 distinct addresses for
`txs` and 100 for `utxo`, `unspent` and `balance`; an empty list, an invalid address (the whole
request is rejected) or a list over the bound gets 400. `txs` windows follow stock (`from`
default 0, `to` default `from + 10`) with `to <= 1000`. The merged history is pending first, then
confirmed by height descending; transactions of different addresses at the same height keep the
request-address order (then the worker's order within an address), not their position in the
block. The order is deterministic for the same input, so paging is stable. `totalItems` is exact while the merged
read stays within a budget of 50000 txids; above it, it is the sum of per-address totals, an upper
bound. Multi-address `utxo` and `unspent` answers are capped at 20000 rows in aggregate; above
that the request gets 413 and the caller should query fewer addresses. The single-address `utxo`
route is not capped.

### Block routes

`blocks` binary-searches heights by time through the index and never scans the chain. A day with
no blocks, a future day or a day before genesis gives an empty `blocks` with a full `pagination`;
a malformed date gives 400. Block times are not strictly monotonic, so a block within about two
hours of a day boundary can be placed on the neighbouring day. `block` loads every transaction of
the block once (cached in a small LRU) to compute `reward`. `rawblock` uses the worker's raw block
and, when the backend does not provide it, falls back to the daemon's `getblock <hash> 0`; the
daemon "not found" code gives 404. The `rawtx` route likewise falls back to `getrawtransaction`.

### Fee, currency and message verification

`utils/estimatefee` takes at most 10 comma-separated targets, each 1 to 1008, default `2`, and
asks the backend for a conservative estimate. A backend that cannot estimate gives `-1` for that
target. This is the normal answer on ZEC, whose Zebra daemon has no `estimatesmartfee`.
`currency` reads only Blockbook's own fiat layer and never calls out; `short` is the coin
shortcut. `messages/verify` needs `address`, `signature` and `message` (body values win over
query values) and answers `{result:true|false}`; a daemon that rejects the input gives
`{result:false}`. When the daemon has no `verifymessage` (ZEC on Zebra) the route answers 501
`Not supported` instead of leaking the daemon message. A route whose backend call is unavailable
for any other reason answers a generic 500 or 503.

### Amounts, errors and limits

All monetary JSON tokens are formatted from integer satoshis with eight-decimal precision and
no floating-point amount arithmetic. Numeric spending fields beyond JavaScript's safe integer
range (`-9007199254740991..9007199254740991`) fail explicitly with a generic 500 response; satoshi
strings such as `value`, `valueIn` and `balanceSat` are not capped. Nil amounts become zero.

Broadcast requires a nonempty, even-length hexadecimal string and a body at most 8 MiB,
including chunked bodies. Malformed, trailing or non-string input receives 400; oversized
bodies receive 413. The daemon decides transaction acceptance. Rejection and unexpected RPC
errors use a generic 500 response; raw daemon messages, URLs, credentials and stack traces are
never returned. A backend error reported inside system-info with no Go error still receives a
generic 503. Error JSON has shape `{status:"error",error:"..."}`. Only the exact worker
"not found" texts give 404; any other worker failure is a generic 500.

All known paths set public CORS headers on every response. Socket.IO and events are not served.
Stock fields the converter never returned (for example a block's `minedBy`) are not emitted.
For a ZEC node only the transparent pool is indexed, so shielded asset semantics are not covered.

## Firo extension

A coin whose chain parser implements `InsightExtension() string` (the
`server.InsightExtensionProvider` interface) gets that named extension on top of the generic
table; today only `bchain/coins/firo` does (`"firo"`). Other coins gain no route and no label:
the extension only adds paths the generic router does not know, replaces `currency`, and
decorates converted transactions. It serves the Firo-specific routes of the stock Firo Insight
(insight-api-zcoin 0.4.5, the API on FIRO-01 port 3001), each with the firod call the stock
controller makes through bitcore-node-zcoin:

| Route | firod call | Response (stock shape) | Cache |
| --- | --- | --- | --- |
| `GET masternode/listmasternodes` | `evoznodelist` | `{result:<firod object>,error:null,id}` | 30 s |
| `GET masternode/listmasternodesarray[/:filter]`, `POST masternode/listmasternodesarray` | `evoznodelist` | `{result:[{collateral,txhash,outidx,ip,proTxHash,address,payee,status,lastpaidtime,lastpaidblock,owneraddress,votingaddress,collateraladdress,pubkeyoperator}],error:null,id}` | 30 s |
| `GET masternode/masternodecount` | `evoznode count` | `{total,enabled}` | 30 s |
| `GET currency` | none (Blockbook fiat rates) | `{status:200,data:{zcoin_usd,btc_usd:0,btc_zcoin:0,bitstamp}}` | fiat layer |
| `GET ext/getmoneysupply` | `gettotalsupply` | `{total}` (coins) | 60 s |
| `GET zerocoin/gettotalzeromint`, `zerocoin/gettotalzerospend`, `sigma/gettotalsigmamint`, `sigma/gettotalsigmaspend` | `getaddressbalance` of the pseudo-address `Zeromint`/`Zerospend`/`Sigmamint`/`Sigmaspend` | `{total}` (spends positive, as stock) | 60 s |
| `GET zerocoin/getrealsupply`, `sigma/getrealsupply` | `gettotalsupply` and the two balances | `{total}` = supply - mints + spends, in stock's float order | 60 s |
| `GET zerocoin/getzerominttxs`, `zerocoin/getzerospendtxs` | `getaddressbalance` and `getaddresstxids` of the pseudo-address | `{balance,txs:[txid]}` | 60 s |
| `GET sparknames` | `getsparknames` | the firod array (`[]` if not an array) | 60 s |

The masternode filter is stock's: `filter` from the body (JSON or form), else the path, else the
query. A comma-separated list matches each item; an item with `-` is an exact
`txhash-outidx`, anything else a substring of the entry's JSON; results keep list order per
item with duplicates dropped. A filter is capped at 4 KiB and 100 distinct items (repeated
items count once); a larger one, in any of the three forms, is a 400 JSON error. Each entry's
JSON is built once per cached list, and the scan stops when the client goes away. On the recorded mainnet list (7287 nodes) the unfiltered array is
byte-identical to stock's apart from `id`, which is the listener's own counter (stock's is a
random JSON-RPC id). Caches are per value and single-flight: an expired value is reloaded by
one firod call whose result or error every concurrent request shares, and a request whose client
goes away stops waiting (the load still fills the cache). When a reload fails the last good value
is served (stale-on-error); with no good value the error is cached for 5 s, so a hung firod is not
called again by every queued request. A firod error is a generic
`{"status":"error","error":"Backend request failed"}` 500 (stock answered with the raw error or
crashed the request); the routes need the daemon RPC and are 501 without it.

`currency` reads the USD rate from Blockbook's fiat layer (`zcoin_usd`, copied to `bitstamp`
as stock does); stock never fills `btc_usd` and `btc_zcoin`, so they stay 0. `configs/coins/firo.json`
sets the ZelCore rates service with CoinGecko id `zcoin` (Firo's id there). The rates service's
Blockbook endpoint answered `coin not found` for `zcoin` when this was written, so the rate is 0
until `zcoin` is enabled there. Stock's own CoinGecko fetch also returns 0 today.

`POST znodes/listznodesarray` (an older insight-api-zcoin route, still about 3.4k calls a year) is
not aliased: stock 0.4.5 answers 404 and so does this listener, so callers see no change. Not
served (Postgres-backed and broken on stock): `richlist`, `sigmastatus`, `lelantusstatus`.

Transactions get stock's labels, keyed by the script's first opcode:

| Script | Label |
| --- | --- |
| input `OP_ZEROCOINSPEND` / `OP_SIGMASPEND` / `OP_LELANTUSJOINSPLIT(PAYLOAD)` / `OP_SPARKSPEND` | `addr` `Zerospend` / `Sigmaspend` / `Lelantusjsplit` / `Sparkspend` |
| input `OP_ZEROCOINTOSIGMAREMINT` | `addr` `Remint`; `value`/`valueSat` = the tx's output value |
| output `OP_ZEROCOINMINT` / `OP_SIGMAMINT` / `OP_LELANTUSMINT` / `OP_LELANTUSJMINT` / `OP_SPARKMINT` / `OP_SPARKSMINT` | `type` `zerocoinmint` / `sigmamint` / `lelantusmint` / `lelantusjmint` / `sparkmint` / `sparksmint`; `addresses` `[Zeromint]` / `[Sigmamint]` / `[Lelantusmint]` / `[Lelantusjmint]` / `[Sparkmint]` / `[Sparksmint]` |

The labels replace the parser's own address labels (for example `LelantusMint`) in Insight output
only; the index and the V1/V2 APIs are unchanged. For a Spark spend, `getsparknametxdetails`
(a name, or firod's "Invalid spark name tx hash", is cached 10 minutes per txid, up to 4096
entries in two generations; any other error, such as "Unknown transaction." for a tx firod
cannot find yet, a transport failure or warm-up, is not cached) decides whether it registers a Spark name; if so
the tx gets stock's `sparkData` `{name,address,validUntil[,additionalInfo]}` and a first output
`{n:0,value:0,type:"sparkname",addresses:["Sparkname"]}` (its `scriptPubKey` carries `hex:""`
and `asm:""`, as stock's), and the outputs are renumbered, as stock does. So on a Spark name
registration tx the tx-detail `vout` numbering is shifted by one from the chain's, exactly as on
stock: a consumer must take outpoints (txid and vout index) from the `utxo` route, not from the
tx detail. Stock makes that call for every transaction; here only Spark spends trigger it.

## Readiness and shutdown

The bind occurs synchronously before startup succeeds. All routes return 503 until an owned
atomic readiness flag is set. With `-sync`, this follows initial index sync, mempool initialization
and its first resync. Known-route OPTIONS remains 204 and method validation remains 405.
Without `-sync`, readiness follows the existing full public API startup point; it does not
certify a current index. Status is also gated because the inherited system-info worker
reads startup state that is changed during synchronization.

At most 32 requests enter handlers concurrently. Excess work receives immediate 503 with
`Retry-After: 1`; there is no waiting queue. A POST body is read (at most 8 MiB, within a 10
second body deadline) before a slot is taken, so a client that trickles a body occupies a
connection but no slot; it gets 400 when the deadline passes. At most 16 bodies are read at once
(503 beyond that), and a declared `Content-Length` above 8 MiB gets 413 before any read. HTTP limits are five seconds for
headers, 30 seconds for reading, 60 seconds for writing and idle connections, and 1 MiB of headers.

The listener does not rate-limit by source. HAProxy in front of it should: set
`option http-buffer-request` on the Insight frontend/backend so a request body is complete before
it is forwarded (a slow client then ties up HAProxy, not the listener), a short
`timeout http-request` (for example 10s) to cut slow headers and bodies, and a per-source
connection and request rate limit (stick-table with `conn_cur` and `http_req_rate`) on the explorer
frontends.

Every main return after startup runs registered cleanup before the deferred database close.
Normal signal shutdown drains Insight before closing the daemon. Admission closes first and
all active calls, including status, are tracked. If HTTP shutdown's deadline expires, connections
close but cleanup still waits for actual handlers before allowing database or daemon closure.
Legacy worker and broadcast methods cannot cancel, so a stalled backend call can extend process
shutdown beyond the HTTP deadline. The service manager's existing stop timeout is a separate
operator consideration.

## Recorded local verification

The routes are covered by fake-worker server tests (`go test -tags unittest ./server/ -run
Insight`, run in the `blockbook-build` image) and by template tests (`go test ./build/tools/`),
which render the actual service output in dev and prod, preserving optional runtime environment
loading, jemalloc settings, backend dependency gating and dev-only pprof, and check that the
BTC and ZEC definitions enable the listener on 9876.

Race checks use a fake query worker, so they establish the exercised listener and lifecycle
behavior, not race freedom of every inherited worker path. Qualifying a real worker with fiat
updates running found a data race: `GetSystemInfo` read the historical fiat timestamps while
`fiat/fiat_rates.go` wrote them. It was found and fixed in `69e8bc1e`: the timestamps are now
accessed through `InternalState` setters and a getter under its existing mutex, and the fix is
guarded by two `-race` tests (`TestGetSystemInfo_FiatTimestampsRace` and
`TestRunHistoricalCycle_InternalStateTimestampsRace`).

## Qualification before production use

Local fake-worker and template tests cover the contract and lifecycle. They do not
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
4. Set `BLOCKBOOK_INSIGHT_HOST` in `/etc/blockbook/blockbook.env`, stop and disable the converter
   on that host (the deploy refuses while it holds the port) and verify the listener on port 9876 behind the trusted proxy: TLS, CORS, 503 readiness,
   saturation, body limits and shutdown under active requests. Apply the HAProxy settings from
   the limits section above (`option http-buffer-request`, `timeout http-request`, per-source
   rate limits) before the listener is public.

   First switch order. The deploy script checks the env file and that the address is assigned
   to the host before it builds, but checks the port holder only after the build, just before it
   stops Blockbook, so the converter keeps serving during the build. Build the package first (or
   run the script once: it builds, then refuses at the port check with nothing stopped), then
   stop and disable the converter, then deploy with `--skip-build`. Port 9876 is dark from the
   converter stop until the new listener is ready, which can take a while since it returns 503
   until `-sync` readiness. A script rollback restores the previous Blockbook but does not
   restart the converter, so after a rolled-back first switch 9876 stays dark until the owner
   re-enables the converter.
5. Retain a recoverable snapshot and agree on canary/rollback steps. Matching schema and unit
   fixtures alone do not prove production database compatibility. No schema migration is added
   here; rollback across any other write migration requires restoring its pre-migration snapshot.

This work performs no deployment, daemon integration, production snapshot rehearsal or source
publication. No production connectivity is required for the local test suite.

The [2026-10-15 forward-port validation record](fork-forward-port-validation-2026-10-15.md)
records exact candidate revisions, local evidence, reproduction commands and outstanding qualification.
