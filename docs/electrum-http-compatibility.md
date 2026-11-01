# electrum-http compatibility listener

ZelCore wallets talk to their "electrum" coins (BTX first) through a small Node HTTP proxy,
[ZelCore-io/electrum-http](https://github.com/ZelCore-io/electrum-http) (`index.js`, port 3456),
which turns query-string GET requests into Electrum protocol calls on an ElectrumX server. The
optional `-electrumhttp` listener answers the same requests from Blockbook's own index, mempool
and backend RPC, so the proxy and its ElectrumX can be retired without a wallet change: old and
new wallet versions keep calling the same URL.

```
https://proxy.btx.zelcore.io/?server=127.0.0.1&port=50002&contype=tls&coin=bitcore&call=<call>&param=<value>
```

It reuses the query worker, mempool and database of the running Blockbook (no second RocksDB
open, no calls to the public API) and the raw daemon access the Insight listener uses
(`server/insight_rpc.go`). It never connects anywhere based on the request: `server`, `port`,
`contype` and `coin` are parsed for compatibility and otherwise ignored.

## Enabling it

The listener is disabled unless `-electrumhttp=host:port` is given. It accepts only Bitcoin-type
chains with eight decimals; startup fails for other parsers, a missing host or a port collision.
A coin config opts in with a pair of fields (both or neither; the rendered port must match):

```json
{
  "ports": { "blockbook_electrum_http": 3456 },
  "blockbook": { "electrum_http_binding_template": "${BLOCKBOOK_INSIGHT_HOST}:{{.Ports.BlockbookElectrumHTTP}}" }
}
```

The generated unit then has `-electrumhttp=${BLOCKBOOK_INSIGHT_HOST}:3456` in `ExecStart`, after
`-insight=...` when the coin also has Insight. The binding is validated like
`insight_binding_template` (see [config.md](config.md)): an explicit host, the configured port,
no whitespace or quoting, and `${BLOCKBOOK_INSIGHT_HOST}` as the only allowed environment
reference, which systemd expands from `/etc/blockbook/blockbook.env` (see [env.md](env.md)). A
literal host such as `127.0.0.1:{{.Ports.BlockbookElectrumHTTP}}` works too. Port 3456 is the
proxy's; take it over only after stopping the proxy on that host. The listener serves plain HTTP;
public TLS belongs to the reverse proxy in front.

`contrib/scripts/deploy-blockbook.sh` has no electrum-http preflight: its
`BLOCKBOOK_INSIGHT_HOST` checks run only for coins with `ports.blockbook_insight`. A coin that
uses `${BLOCKBOOK_INSIGHT_HOST}` for electrum-http alone must have that variable set on the host,
or Blockbook refuses to start.

The listener starts with Blockbook but answers 503 (`Error: Service unavailable`) until the
initial sync and mempool load are done, like Insight. On shutdown it stops admitting requests and
drains the active ones before the database and daemon connection close. At most 32 calls run at
once; more answer 503 `Error: Service busy`.

Only the root path is served: every proxy URL, the backward-compatible forms included, is
`/?...`. A request on any other path is HTTP 404 `Error: Not found` before the rate limiter or
the query parser sees it.

## Request budget

Each request has a budget, so one call cannot fan out into unbounded backend work:

- a 60 s deadline, checked inside the per-transaction and per-input loops
  (`Error: request timed out`);
- at most 2500 backend lookups (daemon RPCs and worker transaction loads); past that the answer
  is `Error: request too large`;
- `nicehistory` reads each transaction once from the daemon (verbose) and once from the worker,
  which resolves every input's value and script from Blockbook's index. The raw previous
  transaction is read only for an input the index cannot resolve, or when the index's P2PKH
  script may stand for a P2PK output (single-push scriptSig); those reads are cached per request
  (at most 256 transactions) and count against the budget;
- `niceentirehistory` keeps the proxy's 30001 in name, but its effective cap is 1000
  transactions: an address with more history answers
  `Error: history too large: <n> transactions, at most 1000 per request` before any transaction
  is loaded. `nicehistory` (31) is unaffected. History calls also stop at ElectrumX's limit
  below, and their mempool lookups count against the budget.

## Rate limit

A per-client token bucket in front of the listener stands in for the proxy's `ddos` module
(`burst: 25, limit: 100`): 100 requests per minute with a burst of 25 by default, tuned with
`<network>_ELECTRUM_HTTP_RATE_LIMIT` (0 disables it) and `<network>_ELECTRUM_HTTP_BURST`. It is
the public server's REST/UI limiter with its client-IP rules (`<network>_TRUSTED_PROXIES`,
Cloudflare ranges, `REST_UI_WHITELIST_IPS`); a request from a local or trusted peer without a
client-attribution header is not limited, so the reverse proxy must send `X-Real-Ip` (or the
request must come through Cloudflare) for the limit to apply per client. A limited request gets
HTTP 429 with a JSON body and `Retry-After` (the `ddos` module also answers 429).

## Request parsing

Parsing follows `index.js` step by step:

1. The whole request target is passed through `decodeURIComponent`; a malformed escape is HTTP 400
   `Error: URI malformed` (the proxy throws, and Express answers 500).
2. The text between the first and second `/?` is parsed like `qs.parse`: `&`-separated pairs,
   `+` as a space, a second percent-decoding pass. A repeated or bracketed key (`call=a&call=b`,
   `call[]=a`) becomes an array, which is never a known call.
3. If `call` or `server` is missing or empty, the raw target is re-split in the old
   `/?server=X?call=Y?param=Z` form, which replaces all three.
4. If `call` is set, is not `height`, and `param` is missing, the raw target is re-split as
   `...&call=Y?param=Z` (the "mobile mistake" form), which replaces `call` and `param`.
5. Missing or empty `call`: `Error: Call is undefined`. Missing or empty `server`:
   `Error: Server is undefined`. The value of `server` is not used.

## Responses

Success is HTTP 200 with a JSON body (`Content-Type: application/json; charset=utf-8`). Failure
is HTTP 200 with a plain-text body `Error: <message>`, as the proxy sends it. Every response
carries the proxy's CORS headers (`Access-Control-Allow-Origin: *`,
`Access-Control-Request-Method: *`, `Access-Control-Allow-Methods: OPTIONS, GET`,
`Access-Control-Allow-Headers: *`) and `X-Content-Type-Options: nosniff`. JSON is written as
JavaScript's `JSON.stringify` writes it: key order is kept and numbers are formatted as JS doubles,
so values that pass through floating-point arithmetic in the proxy come out the same
(`28999999.999999996`).

| call | param | answer |
|------|-------|--------|
| `balance` | address | `{"confirmed":<sat>,"unconfirmed":<sat>}`; unconfirmed is the mempool delta and may be negative |
| `history` | address | like `entirehistory`, but more than 200 entries answer the last 100 |
| `entirehistory` | address | `[{"tx_hash","height"}...]` in blockchain order (ascending height, block position inside a block), then mempool entries `{"tx_hash","height","fee"}` with height `0`, or `-1` when an input is unconfirmed |
| `transaction` | txid | raw transaction hex as a JSON string |
| `transactionnonverbose` | txid | same as `transaction` |
| `transactionverbose` | txid | the daemon's `getrawtransaction <txid> 1` object, key order kept |
| `utxo` | address | first 600 of `[{"tx_hash","tx_pos","height","value"}...]`: confirmed outputs in blockchain order, then mempool outputs with height `0`; outputs spent in the mempool are left out |
| `niceutxo` | address | the confirmed rows of the first 600 `utxo` rows as `{"txid","vout","scriptPubKey","satoshis","height"}`, `scriptPubKey` being the address's output script |
| `broadcast` | raw tx hex | the txid as a JSON string |
| `height` | none | `{"hex":<80-byte header>,"height":<n>}` of Blockbook's indexed tip (`blockchain.headers.subscribe`) |
| `header` | height | `{"block_height","version","prev_block_hash","merkle_root","timestamp","bits","nonce"}` (the legacy `blockchain.block.get_header`) |
| `nicehistory` | address | the 31 newest history entries, newest first, as described below |
| `niceentirehistory` | address | the same with up to 30001 entries |

`nicehistory` entries have the proxy's fields in its order: `txid`, `version`, `locktime`,
`vin`, `vout`, `time`, `confirmations`, `valueInSat`, `valueOutSat`, `fees`, `height`, `hex`.
`txid`, `version`, `locktime` and `hex` come from the verbose transaction; `time` and
`confirmations` too, with the current time and `0` for a mempool transaction; `height` is the
history entry's (0 or -1 in the mempool). Each `vin` is
`{txid, n, script, sequence, scriptSig:{hex, asm}, addr, value, valueSat, satoshis}`: `script`
and `asm` are bitgo-utxo-lib's `toASM` of the scriptSig (pushes as hex, not bitcoind's ASM),
the value is the spent output's (from Blockbook's index), `value` is `1e-8 * satoshis`, and `addr` is the
previous output's address for P2PKH and P2SH outputs only ("" for witness and other outputs).
Coinbase inputs are left out, so a coinbase transaction has `"vin":[]`. Each `vout` is the
verbose output with `satoshi` and `valueSat` (`value * 1e8`) appended; `valueOutSat` sums those,
`valueInSat` sums the input values, and `fees` is the inputs minus the outputs, all in the same
floating-point order as `index.js`.

The wallet uses `balance`, `utxo`, `niceutxo`, `nicehistory` and `broadcast`.

### Errors

| case | body |
|------|------|
| address missing, invalid or of another network | `Error: <param> has no matching Script` (`undefined` when missing) |
| txid not 64 hex digits | `Error: <param> should be a transaction hash` (`None` when missing) |
| header height not a non-negative integer | `Error: <param> should be a non-negative integer` |
| header height above the tip | `Error: height 1,890,701 out of range` |
| daemon RPC error (unknown tx, ...) | `Error: daemon error: DaemonError({'code': -5, 'message': '...'})` |
| broadcast refused by the daemon | `Error: the transaction was rejected by network rules.\n\n<daemon message>\n[<raw tx>]` |
| more than ElectrumX's history limit (10100 confirmed txs) | `Error: history too large` |
| unknown call | `Error: Unknown call` |
| request budget exhausted | `Error: request too large` / `Error: request timed out` |
| niceentirehistory over 1000 transactions | `Error: history too large: <n> transactions, at most 1000 per request` |
| path other than `/` | HTTP 404 `Error: Not found` |
| any other failure (backend unreachable, ...) | `Error: internal error`; the cause is logged, not sent |

The address, txid and height messages are the ones the live proxy returns; the daemon and
broadcast messages are ElectrumX's formats.

## Known differences from the proxy

- **`height` and `header` work.** The live BTX proxy answers
  `Error: 1 argument passed to method blockchain.headers.subscribe taking at most 0` and
  `Error: unknown method "blockchain.block.get_header"` (its ElectrumX dropped the old method and
  rejects the extra argument the client sends). The listener answers the shapes those methods
  had. No wallet calls either.
- **niceentirehistory** is capped at 1000 transactions (see Request budget); the proxy tries up
  to 30001. No wallet calls it.
- **Other paths.** The proxy answers on any path; the listener only on `/`.
- **Unknown call.** The proxy never answers (no default case); the listener answers
  `Error: Unknown call`.
- **`coin` is ignored.** The proxy encodes addresses with the network named by `coin`, so its
  backward-compatible URL forms, which carry no `coin`, reject every BTX address. The listener
  always uses the chain's own network, so those forms work.
- **Input order and completeness in `nicehistory`.** The proxy pushes inputs, and transactions,
  in the order its parallel lookups finish, and can answer before a transaction's last inputs
  arrive. The listener lists inputs in transaction order, transactions newest first, and always
  includes every input.
- **P2PK outputs.** Blockbook indexes a P2PK output under the matching P2PKH address, ElectrumX
  under its own script hash, so such outputs (early coinbase outputs) appear in the P2PKH
  address's `balance`, `history` and `utxo` here and not on the proxy.
- **Mempool entry order.** ElectrumX leaves the order of mempool entries unspecified; the
  listener puts height `0` entries before `-1` entries, each sorted by txid.
- **Raptoreum.** The proxy's Raptoreum-only coinbase special case is not reproduced (it depends on
  `coin`, and Raptoreum is not a Bitcoin-type eight-decimal chain here).
- **OPTIONS** answers 204 with the CORS headers and runs no call (the proxy runs the call for any
  method). Methods other than GET, HEAD and OPTIONS answer 405.
- **A missing broadcast param** is sent to the daemon as an empty string, and the bracket of the
  refusal shows `[None]`.
- **Repeated parameters** (`param=a&param=b`) are joined with `,` as JavaScript would print the
  array; ElectrumX would print a Python list.

## Tests

`server/electrumhttp_test.go` (`go test -tags unittest ./server/ -run ElectrumHTTP`) feeds stubbed
index, mempool, worker and daemon data and compares the answers with bodies captured from the
live BTX proxy on 2026-11-01 (`server/testdata/electrumhttp/`), byte for byte: `balance`,
`utxo`, `niceutxo`, `history` (including the 100-of-719 truncation), `entirehistory`,
`nicehistory`, `niceentirehistory`, `transaction`, `transactionnonverbose` and
`transactionverbose`. Further table tests cover the query-parsing forms, the error bodies, the
600/200/31 limits, ElectrumX's history limit, mempool entries (height 0 and -1, fees, mempool
outputs in `utxo` but not `niceutxo`), coinbase/P2SH/witness inputs and mempool times in
`nicehistory`, `height`/`header`, `broadcast` (stubbed; nothing is sent to a network), the HTTP
headers, readiness, the rate limit and the JavaScript number and ASM formatting.
