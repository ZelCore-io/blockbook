# insightcompare

A/B tool for the native Insight listener. It requests the same paths from a reference (the Node
converter on ZEC-01/BTC, or stock Insight on FIRO/ZCL) and from the new listener, diffs the JSON,
and writes a JSON report plus a short markdown summary. Standard library only.

It sends GET, OPTIONS and a few deliberately malformed POSTs. It never broadcasts: every
`POST tx/send` is checked before anything is sent, and a body whose `rawtx` is even-length hex (or
anything else that could be a transaction) makes the tool refuse to start. POST is allowed only on
`tx/send`, `addrs/utxo`, `addrs/txs` and `messages/verify`.

CI skips `contrib`, so run the tests by hand:

```
go test -count=1 -race ./contrib/insightcompare/...
```

## Reaching the hosts

Run it from a laptop through SSH tunnels, or from a host on 10.100.0.x. Base URLs include the API
prefix (`/api` or `/insight-api`; the listener serves both).

```
ssh -N -L 19001:10.100.0.<zec01>:9876 -L 19002:10.100.0.<zec02>:9876 <jump-host>
```

## Typical runs

1. Discover real sample paths from recent blocks (no address is hardcoded). `-seed` points at an
   Insight base (the listener under test is fine, it serves block-index, block and tx). Supply one
   valid never-used address with `-empty`:

```
go run ./contrib/insightcompare -seed http://127.0.0.1:19002/api -blocks 3 \
  -empty <unused-valid-address> -seed-out zec-paths.txt
```

   Read the `# note:` lines at the top of `zec-paths.txt`: they say which categories were not
   found (no OP_RETURN tx, no address past 1000 txs, no pending address). Raise `-blocks` or
   `-seed-txs`, or pass `-pending addr1,addr2` for addresses with a mempool tx. For Blockbook
   public-API discovery use `-seed https://<blockbook> -seed-kind blockbook` (it also reads
   `/api/v2/mempool` when present); `-mempool-url <blockbook root>` adds that mempool discovery to
   an Insight seed.

2. Compare ZEC-02 (native, `:9876`) against ZEC-01 (converter, `:9876`):

```
go run ./contrib/insightcompare -mode converter \
  -ref http://127.0.0.1:19001/api -new http://127.0.0.1:19002/api \
  -paths zec-paths.txt -out zec-ab.md -load 20
```

3. Stock-only routes against stock Insight on FIRO/ZCL (`:3001`), numeric strings equal numbers:

```
go run ./contrib/insightcompare -mode stock \
  -ref http://127.0.0.1:13001/insight-api -new http://127.0.0.1:13002/api \
  -paths firo-paths.txt -out firo-ab.md
```

`-seed ... -ref ... -new ...` without `-paths` seeds and compares in one run.

Outputs: `-out` (markdown summary, also printed) and `-json` (default: `-out` with `.json`), the full
machine-readable report with every finding, latency numbers and the rule table. Exit status: 1 if
any `missing` or `changed` finding exists, 2 on usage or safety errors, else 0.

## Flags

| Flag | Meaning |
| --- | --- |
| `-ref`, `-new` | base URLs |
| `-paths` | one request per line: `GET path` or `POST path <json>`; `#` comments |
| `-mode` | `converter` (default) or `stock` |
| `-out`, `-json` | summary and JSON report paths |
| `-concurrency` | paths compared in parallel (default 4) |
| `-rate` | global requests per second across both sides (default 8, 0 = unlimited) |
| `-timeout` | per request (default 30s) |
| `-load N` | latency phase: N parallel GETs per side per path (max 64), p50/p95 per side. The listener admits 32 concurrent requests and answers 503 beyond that by design |
| `-load-paths`, `-load-pause` | paths used by the latency phase (default 12) and pause between them |
| `-ignore` | comma-separated JSON pointers to skip; trailing `*` is a prefix, `**/name` a suffix |
| `-origin` | Origin header for the OPTIONS checks |
| `-seed`, `-seed-kind`, `-blocks`, `-seed-txs`, `-seed-out`, `-empty`, `-pending`, `-mempool-url` | path discovery |

`POST tx/send @oversize:9437184` in a paths file sends a generated body of that many bytes whose
`rawtx` is not hex (the listener's limit is 8 MiB).

## Rules

Findings have a kind. `missing` (key in ref, absent in new) and `changed` fail the run. `extra`
(key only in new, route absent on the reference) and `expected` are reported, not failing.

- Non-JSON on one side, or a different HTTP status: `changed`. Error bodies compare status code and
  `status` key only. Exceptions: a 501 from the listener is `expected` (E9), a reference 404 against
  a listener 200 is `extra` (route the converter lacks; check it against stock).
- `confirmations` and the confirmation ETA fields are ignored everywhere. `info.blocks`,
  `blockChainHeight`, `height`, `syncPercentage` allow +-3 on `status` and `sync`. `totalItems`,
  `txApperances`, `unconfirmedTxApperances` allow +-2. `currency` `data.rate` is checked for
  presence only.
- Numbers compare by value (the listener writes 8-decimal tokens). Mode `stock` also equates
  numeric strings with numbers.
- `items` and `txs` arrays match by `txid`; only txids on both sides are compared and count or
  window differences are `expected`. Order of common txids is checked (ties inside a block are
  expected). Disjoint first pages fail. UTXO arrays match by `txid:vout`. `blocks` rows match by
  `hash`. The `transactions` list compares as a set.
- Each page of every path also gets an `OPTIONS`: `Access-Control-Allow-Origin` must be present on
  both sides (missing on new fails). The `Link` header of the new side is recorded.
- Declared expected differences (table `expectedRules` in `compare.go`, each cited to the Task 8
  brief or spec): E1 asm address text, E2 vin asm absent, E3 history count/`to`/`totalItems`, E4
  blocks rows without `difficulty`, E5 `immature` absent, E6 `minedBy` absent, E7 zero vin
  `vout`/`sequence`, E8 deep paging from >= 1000, E9 ZEC estimatefee -1 and messages 501, E10 list
  drift between the two requests.

## Limits to know

Both sides are requested at the same moment, but a new block or pending transaction can still land
between the two answers, so a single `changed` on a busy address or the chain tip should be re-run
before it is believed. The tool does not model per-coin differences beyond the rules above; use
`-ignore` for pointers the owner has already decided to accept.
