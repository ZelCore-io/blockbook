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
they run on separate hosts. Firo's listener takes port 3001 from the stock Insight
(bitcore `insight-api-zcoin`), which HAProxy already targets for explorer.firo, so only the new
blockbook.firo hostnames need proxy changes. bitcore must be stopped first, and on our Firo hosts
firod runs as bitcore's child, so firod moves to its own service before the switch. Zclassic's
and Zero's listeners likewise take port 3001 from the stock Insight that serves explorer.zcl and
explorer.zer, so the stock Insight must be stopped before Blockbook starts. Axe's listener also
uses port 3001 (new install, explorer.axe.zelcore.io; no stock Insight to replace). BitcoinZ's and
Komodo's listeners take port 3001 from the stock Insight behind explorer.btcz and explorer.kmd the
same way as Zclassic and Zero. The listener serves plain HTTP; public TLS and perimeter request
policy belong at the existing proxy, and the private interface should be reachable only by the
trusted proxy.

## Explorer UI on the listener

A coin may also serve the normal Blockbook explorer on the Insight port, so one hostname (for
Firo, `explorer.firo.zelcore.io` behind HAProxy) answers both the Insight API and the UI. It is
opt-in per coin with `"blockbook": { "insight_ui": true }`, which adds `-insightui` after
`-insight=...` in the generated unit; the build rejects it without the Insight listener, and
Blockbook refuses to start with `-insightui` but no `-insight` or `-public`. Only
`configs/coins/firo.json`, `configs/coins/zclassic.json`, `configs/coins/zero.json`, `configs/coins/axe.json`, `configs/coins/bitcoinz.json` and
`configs/coins/komodo.json` set it; BTC and ZEC keep the
listener exactly as before (unknown paths are the Insight JSON 404).

With it on, the Insight router still answers first. Every route in the table below, under
`/api/` and `/insight-api/`, behaves exactly as without the flag, including OPTIONS and 405, and
where both servers know a path (`/api/tx/:txid`, `/api/block/:hash`, `/api/block-index/:height`,
`/api/rawtx/:txid`, `/api/status` and so on) Insight wins. Any other path under `/api/` or
`/insight-api/` (and `/api`, `/api/`, `/insight-api` themselves) is the Insight JSON 404 with the
Insight CORS headers, as on stock, except Blockbook's versioned APIs `/api/v1/...` and
`/api/v2/...`. So `/api/nosuchroute` no longer reaches the public server's `/api/` status page,
and Blockbook's unversioned API paths (`/api/xpub/`, `/api/balancehistory/`, `/api/sendtx/`, ...)
are not served on this listener; use their `/api/v2/` forms. Every other request is handed
in-process to the public server's own handler (`PublicServer.Handler`): `/`, `/tx/`, `/address/`,
`/block/`, `/blocks`, `/search`, `/static/`, `/api/v2/...`, `/api/v1/...`, `/websocket`,
`/robots.txt`, `/sitemap.xml` and the rest. A path neither knows gets the public server's 404 page.

- **Limits.** A fallthrough request is dispatched before Insight admission. It takes none of the
  32 concurrency slots or 16 body-reader slots, does not wait for Insight readiness and gets none
  of the Insight CORS or `Link` headers. The public REST/UI rate limiter applies to it, as on the
  public port.
- **Timeouts.** The listener's 60 s `WriteTimeout` is lifted for that request
  (`http.ResponseController`), matching the public server, which has no write timeout. The
  30 s request read timeout is the same on both servers. A websocket upgrade clears the
  connection deadlines, so websocket clients stay connected through the listener port (a proxy
  in front still applies its own idle limits: HAProxy cuts an idle websocket at `timeout client`/
  `timeout server` unless `timeout tunnel` is set on that backend). The
  connection-level limits stay the listener's: 5 s to send headers (public: 10 s) and 60 s
  keep-alive idle (public: 120 s).
- **Proxy and TLS.** The listener is plain HTTP behind the proxy. Nothing in the public handler
  depends on `r.TLS` except the fallback base URL of robots/sitemap/llms.txt, which honours
  `X-Forwarded-Proto` and is not used at all once `public_url` is set. No cookie is marked
  `Secure`. The websocket origin check compares the `Origin` header with
  `<COIN>_WS_ALLOWED_ORIGINS` (see [env.md](env.md)); if that list is set, add the second
  hostname to it. Client IPs for the rate limiter come from the same trusted-proxy headers as on
  the public port.
- **SEO.** The UI on the second hostname is duplicate content. With `public_url` set
  (`https://explorer.firo.zelcore.io` for Firo, the hostname that already resolves), every page served through the listener carries
  `<link rel="canonical">`, `og:url` and JSON-LD for the main explorer host, and `robots.txt`,
  `sitemap.xml` and `llms.txt` advertise only that host. Without `public_url` they would echo the
  request's own Host, so set it whenever `insight_ui` is on.
- **Shutdown.** The listener drains in-flight UI requests with its own deadline before the
  public server stops; websocket connections belong to the public websocket server and are
  drained by its shutdown, before the database closes.

## HTTP contract

Every route is served under both `/api/` and `/insight-api/`. Empty segments right after the
prefix are dropped before matching, so `/api//addr/A` is `/api/addr/A`: stock serves that form
(Express mounts the API router with an optional slash after the prefix), and about 5% of ZCL's
stock traffic uses it. Nothing else is normalized: an empty segment anywhere else is still an
empty argument (`/api/addr//balance` and `/api//addr//balance` are 400, never the address
`balance`), `.` and `..` are ordinary segments that match no route, a doubled slash before the
prefix (`//api/...`) is not the prefix, and with `-insightui` a request handed to the public
server keeps its path as sent. OPTIONS on any known path is 204,
a known path with the wrong method is 405 with `Allow`, and unknown paths are 404 (or, with
`-insightui`, go to the public explorer as described above, except unknown paths under the Insight
prefixes, which stay the Insight 404).

| Method and path | Response |
| --- | --- |
| `GET status` | `{info:{blocks,version,protocolversion,difficulty,errors,network,testnet,relayfee}}`: `network` (`livenet`, `testnet`, else the daemon's chain name) and `testnet` from the daemon's chain, `relayfee` (coins/kB) from `getnetworkinfo`, cached 10 minutes; each is left out when unknown (no chain name, no daemon RPC or a failed call), and stock's `timeoffset`, `connections` and `proxy` are not served. `?q=getDifficulty` gives `{difficulty}`, `?q=getBestBlockHash` the backend best hash, `?q=getLastBlockHash` `{syncTipHash,lastblockhash}` from the index tip |
| `GET sync` | `{status,blockChainHeight,syncPercentage,height,error,type}`; `finished` only when Blockbook is in sync, `blockChainHeight` is the daemon height and `height` the index height |
| `GET peer` | `{connected:true,host:"127.0.0.1",port:null}` |
| `GET version` | `{version}` of this build |
| `GET blocks?blockDate=YYYY-MM-DD&limit=N&startTimestamp=T` | `{blocks,length,pagination}`; default and maximum `limit` 200 |
| `GET block/:hash` | Block with txids, reward (coinbase outputs minus block fees), confirmations and `isMainChain` |
| `GET rawblock/:hash` | `{rawblock}` hex |
| `GET block-index/:height` | `{blockHash}` |
| `GET tx/:txid` | Transaction in the superset shape below. `spentTxId`, `spentIndex` and `spentHeight` come from the extended index when the coin has one (BTC, `-extendedindex`); without it (ZEC, Firo) `spent` is still true/false but `spentTxId`, `spentIndex` and `spentHeight` are null, because finding a spender would walk the whole history of each spent output's address (the Firo dev fund has over 900k txs). Turning on `-extendedindex` needs a full resync. Stock's special-tx payload fields (`extraPayload`, `cbTx`, `proReg`, `proUpServ`, ...) are not served: Blockbook does not keep them in the index, and this route does not request the daemon's verbose JSON |
| `GET rawtx/:txid` | `{rawtx}` hex |
| `GET txs?block=<hash>&pageNum=N` | `{pagesTotal,txs}`, 10 transactions per page |
| `GET txs?address=A&pageNum=N` | `{pagesTotal,txs}`, 10 per page, newest first with pending first |
| `POST tx/send` | JSON or form `{rawtx:"..."}` to `{txid:"..."}` |
| `GET addr/:address[/:noTxList]` | Balance summary with explicit zeros, string `balanceSat`, numeric other satoshi fields, `txApperances`, `unconfirmedTxApperances` and a `transactions` txid list (default window 0..1000) |
| `GET addr/:address/balance`, `totalReceived`, `totalSent`, `unconfirmedBalance` | Bare satoshi integer, as stock Insight. The converter answered the full address summary object on these paths; this is expected difference E11 in `contrib/insightcompare` |
| `GET addr/:address/utxo` | Array of address, txid, vout, `scriptPubKey`, amount, satoshis, height, confirmations and coinbase when known; the full set, not capped. `scriptPubKey` is the hex output script of the address's index descriptor (no per-output lookup), which is the output script for P2PKH, P2SH, witness and Firo exchange addresses. Outputs the index folds into another descriptor show that descriptor's script: a P2PK output shows the P2PKH script (stock: the P2PK script), and a Firo Spark name fee output shows its leading P2PKH script, not the full script with the Spark name payload. It is left out if the address does not parse |
| `GET addrs/:address/txs?from=0&to=50` | `{totalItems,from,to,items}` for one address; exclusive-end offsets |
| `GET addrs/A,B/txs`, `POST addrs/txs` | Merged history of at most 20 addresses, same shape |
| `GET addrs/A,B/utxo`, `GET addrs/A,B/unspent`, `POST addrs/utxo` | Concatenated unspent rows of at most 100 addresses; `unspent` returns confirmed outputs only |
| `GET addrs/A,B/balance` | `{balance,unconfirmedBalance}` in satoshis, summed over at most 100 addresses |
| `GET utils/estimatefee?nbBlocks=2,3` | `{"2":<coins/kB>,...}`; `-1` when the daemon cannot estimate |
| `GET currency` | `{status:200,data:{rate:<USD>,short:"<TICKER>"}}`; rate 0 without a fiat source (Firo: stock shape, see [Firo extension](#firo-extension)) |
| `GET`/`POST messages/verify` | `{result:<bool>}` from the daemon's `verifymessage` |
| `GET statistics/circulating-supply` | The coin supply in coins as a bare number (`text/html; charset=utf-8`, as stock's `res.send`), or `{"circulatingSupply":"<n>"}` with `?format=object`; read from the daemon, cached 10 minutes; the UTXO-scan fallback is opt-in per coin, otherwise a daemon without a cheap source gives the unknown-route 404 (see [Circulating supply](#circulating-supply)) |

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
`scriptPubKey{value,n,spent,hex,addresses,isAddress,type,asm}`. `scriptPubKey.type` is a coin
label when there is one (Firo), else classified from the script with stock's (bitcore address)
names: `pubkeyhash` (also for P2PK, which is indexed under its P2PKH address), `scripthash`,
`witnesspubkeyhash`, `witnessscripthash`, `taproot`, plus bitcoind's `nulldata` and `multisig`,
which stock leaves out; it is omitted for other scripts. The converter's top-level `type` is
unchanged. Coins on the Zcash parser also get stock's shielded fields and shielded-aware
`fees` (see [Zcash extension](#zcash-extension)). History items also set `isOwn` on
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
no floating-point amount arithmetic. Coin amounts (`balance`, `amount`, `value`) and numeric
satoshi fields (`satoshis`, `valueSat`, `totalReceivedSat` and the like) are written as the exact
decimal of the integer, whatever its size, so an amount beyond JavaScript's safe integer range
(2^53 satoshis, about 90,071,992.5 coins) is still served: BitcoinZ addresses hold more than that.
Stock Insight wrote plain JavaScript numbers and lost precision there; a JavaScript client parsing
these numbers still rounds them, but no request fails. Satoshi strings such as `value`,
`valueIn` and `balanceSat` are not capped either. Nil amounts become zero.

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

### Circulating supply

`statistics/circulating-supply` is the only stock `statistics/*` route served; it is generic
(no coin branch) and reads the daemon with the cheapest source it offers:

1. `getblockchaininfo` `chainSupply.chainValueZat` when it is present and monitored (zcashd 5+,
   Zebra);
2. else the sum of `getblockchaininfo` `valuePools[].chainValueZat` when they include the
   `transparent` pool;
3. else, only for a coin that opts in, `gettxoutsetinfo` `total_amount` (the transparent UTXO
   set) plus every shielded pool in `valuePools` (zcashd before 5, zclassicd, fluxd). A coin
   without `valuePools` (Bitcoin, Firo, ...) would get `total_amount` alone: unspent outputs,
   without provably unspendable ones.

The third source is a full scan of the daemon's UTXO set, which anyone could trigger on a public
listener, so it is off by default. A coin enables it with
`"blockbook": { "block_chain": { "additional_params": { "insight_supply_utxo_scan": true } } }`
(rendered into `blockchaincfg.json` and read by Blockbook at startup); only
`configs/coins/zclassic.json` sets it. Without the opt-in, and on a daemon with neither
`chainSupply` nor a transparent value pool (Bitcoin, Firo, Flux), the route is the unknown-route
404 `{"status":"error","error":"Route not found"}`, exactly as before the route existed, and
`gettxoutsetinfo` is never called; only the cheap `getblockchaininfo` is. Without daemon RPC it
is the same 404.

All arithmetic is in integer satoshis; the answer is formatted with trailing zeros trimmed
(`21`, `9888988.88890124`). A reported pool that the daemon does not monitor makes the total
unknown: the route answers 501 rather than a short number. It is also 501 when an opted-in
daemon has no `gettxoutsetinfo`. Other failures are the generic 500.

The value is cached 10 minutes and loaded single-flight (one daemon call shared by concurrent
requests, stale value served if a refresh fails), so an opted-in coin runs at most one scan per
10 minutes. A failed load (and the no-source answer) is remembered for 5 minutes instead of the
usual 5 seconds: older zcashd-lineage daemons (zclassicd 2.1) hold `cs_main` during the scan, and
a scan that outlives Blockbook's RPC timeout keeps running in the daemon, so it must not be
restarted by every request.

**ZCL differs from stock.** Stock insight-api computes the value from a hardcoded per-coin
emission formula (its `SupplyHelper`), not from the chain. On ZCL the deployed stock answered
`41249575` at height 3,302,466, which is exactly `(height - 2500) * 12.5`: a flat 12.5 ZCL per
block with no halvings, about twice ZCL's 21M cap. The listener's figure is what the chain holds and will be much lower. Consumers of this
route (about 215 requests a day on ZCL, likely market-data fetchers) will see that change.

Not served (Insight 404 with CORS headers): the other stock `statistics/*` routes
(`total-supply`, `supply`, `fees`, `network-hash`, `pools`, `pools-last-hour`, `transactions`,
`outputs`, `difficulty`, `total`, `balance-intervals`, `richer-than`,
`richest-addresses-list`), `/api/supply`, and `markets/info` (about 15k requests together in
18 months of ZCL stock logs). They need stock's Mongo statistics or a market feed.

## Zcash extension

Every coin on the Zcash parser (`bchain/coins/zec`: Zcash, Zclassic, Zero, Flux, BitcoinZ, and
Komodo through `bchain/coins/kmd`, which embeds the Zcash parser) gets the `"zcash"` extension. It adds no route; it
adds the fields the Zcash-family stock Insight (bitcore-node `getDetailedTransaction` with
insight-api `transformTransaction`, the same on the zelcash, zclassic and bitcoinz branches and
on insight-api-komodo) puts on every tx object, on `tx`, `txs?block=`/`txs?address=`, address
history and multi-address history, after `vout` and in stock's order:

| Field | When | Value |
| --- | --- | --- |
| `vjoinsplit` | version >= 2 | `[{vpub_old,vpub_new,n}]`, vpub as eight-decimal strings (`"0.50000000"`, stock's `toFixed(8)`); `[]` when there are none. A v1 tx has no key, as on stock |
| `fOverwintered` | the daemon prints `overwintered` | its boolean |
| `nVersionGroupId`, `nExpiryHeight` | overwintered | numbers (`versiongroupid` hex parsed, as stock's `parseInt(.., 16)`: `892f2085` is 2301567109) |
| `valueBalance`, `spendDescs`, `outputDescs` | overwintered and version >= 4 | coins as a number; the daemon's `vShieldedSpend`/`vShieldedOutput` as printed |
| `bindingSig` | as above, when the daemon prints a nonempty one | hex string |

`fees` becomes stock's: transparent in − transparent out + Σ(`vpub_newZat` − `vpub_oldZat`) +
`valueBalanceZat`, so a t→z or z→t tx shows the fee it paid (the generic value, clamped
transparent in − out, shows the whole shielded amount or 0 there). The zat fields are used when
the daemon prints them, else the coin values, parsed exactly. On a Zcash v5 tx the Orchard
`valueBalanceZat` is added too, which stock (written before Orchard) does not do. A coinbase, or
a tx whose input value is unknown, keeps the generic `fees`; a negative result is shown as 0.
`valueIn` stays the converter's transparent satoshi string.

The fields come from the daemon's verbose tx JSON. A pending tx carries it from the worker; for a
confirmed tx the tx cache keeps only the packed tx, so the listener calls `getrawtransaction
<txid> 1`: for a page of history all missing txs at once, at most 8 calls in parallel, and it
keeps the extracted fields (they depend only on the tx bytes) in a 32 MiB cache, so wallet
polling of the same pages calls the daemon once per tx. A failed call fails the request with a
generic 500. Without daemon RPC the fields are left out and `fees` stays generic.

## Firo extension

A coin whose chain parser implements `InsightExtension() string` (the
`server.InsightExtensionProvider` interface) gets that named extension on top of the generic
table; today `bchain/coins/firo` does (`"firo"`) and `bchain/coins/zec` (`"zcash"`, see
[Zcash extension](#zcash-extension)). Other coins gain no route and no label:
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
item with duplicates dropped. A JSON array of strings as the body `filter` gets what stock's
code does with it: the elements joined by `,` are one substring of the entry's JSON (no split, no
exact match), so `["a","b"]` is usually 200 `[]`, `[]` returns all nodes and a one-element array
works like that string as a substring. An array with a `","` or `"-"` element (stock throws and
answers 200 `{}`) or with non-string elements, and any other non-string filter, is a 400. A filter is capped at 4 KiB and 100 distinct items (repeated
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
the tx gets stock's `sparkData` `{name,address,validUntil[,additionalInfo]}`. The newer upstream
explorer (insight-api-firo master) also inserts a synthetic first output
`{n:0,value:0,type:"sparkname",addresses:["Sparkname"]}` and renumbers the outputs; the deployed
stock insight-api-zcoin 0.4.5 does not, and neither does this listener, so `vout[].n` is always
the on-chain output index and can be used as an outpoint. Stock makes that call for every
transaction; here only Spark spends trigger it.

The Spark name fee output itself (`sparknamefee` in firod: a P2PKH script followed by
`OP_SPARKNAMEID <name> OP_DROP <spark address> OP_DROP`, paid to the dev fund on type 11
registrations since about block 1,300,000) is indexed by the Firo parser under its P2PKH address,
so it counts in that address's balance, history and utxos, as in firod's address index. In Insight
output it keeps its full `hex` and `asm` (`OP_SPARKNAMEID` shows as `OP_UNKNOWN225`), its
`addresses` are the P2PKH address and `scriptPubKey.type` is `pubkeyhash`, as stock. An exchange
output's `scriptPubKey.type` is `expubkeyhash`. A database indexed before this parser change keeps
these outputs under the full script (no address): it needs a full resync for the dev-fund
balance, history and utxos to be right. `-rollback` cannot reach back to the first Spark name
fee output (between blocks 1,300,000 and 1,350,000), because Firo keeps the per-block data needed
to disconnect only for the last `block_addresses_to_keep` (300) blocks.

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
