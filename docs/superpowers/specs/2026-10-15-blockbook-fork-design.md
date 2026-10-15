# Zelcore Blockbook branding and native Insight design

Status: implementation authorized by the user on 15 October 2026: “Yes proceed like you recommend … insight handler inside blockbook … leverage subagents with gpt6.1 sol.” This records the two separately reviewable chunks discussed and approved in chat. It does not authorize a host deployment.

## Scope and baseline

Start from deployed BTG commit `b14641d979241b93108025f2a72b4517ab4db260`, Blockbook 0.5.0. Keep branding and native Insight in separate commits/branches. Do not import the 940 unrelated commits from current master. GRS deployed bases `9dfbb10c` and `dcdb2bd2` have identical database/parser/module code; prepare branding backport instructions without upgrading either backend daemon. No indexing, parser, database schema, dependency-version, or coin backend changes are part of this work.

All work is local. No Ansible, SSH, production connectivity tests, host changes, deployments, master merges, or release publication. Existing root AGENTS testing guidance applies where tooling exists; the deployed base predates its wrapper scripts. Run equivalent local `go test -tags unittest` commands. Record unavailable checks honestly. A matching schema number and fixture tests do not prove production database compatibility.

## Chunk 1: existing explorer branding

Use the existing server-rendered explorer. Replace the Trezor header/title with Zelcore text using existing Bootstrap styles. Add `https://zelcore.io`, `https://docs.zelcore.io`, and a “Powered by Flux” link to `https://runonflux.com`. Preserve the SatoshiLabs attribution, COPYING, required notices, Terms of Use link, search, coin label, and transaction navigation. Do not invent a new terms policy. Adjust the embedded About text to describe the Zelcore fork and credit Blockbook upstream.

Every page must have a visible source link for the fork. Clean release builds embed a full Git commit SHA and link to `https://github.com/ZelCore-io/blockbook/tree/<sha>`; the status commit link must also point to the fork. A development build may show a clearly labelled repository fallback rather than a broken unknown/dirty commit URL. The release instructions must require publishing the matching source commit, build scripts, and required corresponding source before making the service public. No claim of AGPL compliance is made for an unpublished local development build.

Keep this change easy to cherry-pick. Add only the small template-data/helper plumbing needed for an accurate source link. No new CSS framework, logo downloads, marketing trackers, article feed, banner network, TS frontend, or dashboard. The central campaign/feed and shared TS explorer remain later design chunks; the TS explorer still requires research and a Figma prototype first.

The old Dockerfile fetches unpinned upstream HEAD just to preload modules. Remove that preload; the existing prepare-sources step already downloads modules from the checkout. Forward explicit full revision build metadata through binary and Debian build paths, retaining dirty/development identification for ordinary local builds. Provide release commands that reject a dirty checkout; do not publish artifacts or change backend package metadata.

Verification: existing rendered-HTML expectations, small source-link rendering tests where needed, debug and production template parsing, existing server/database unit tests, and local desktop/mobile preview if feasible. No implementation-mirroring tests for static link copy.

## Chunk 2: native Insight compatibility

Use one Blockbook process and its existing query worker/database. Add `server.InsightServer`; inject a narrow worker interface and a broadcast function. The implementation must not open another RocksDB instance, call the public Blockbook HTTP service, introduce a Node service, or change V1/V2 API data structures.

Initial eligibility: Bitcoin-type chains with eight decimals, including BTG and GRS. Refuse startup for other chain types when enabled. Keep the listener disabled by default. Add `-insight=<host>:<port>`, independent from `-public`; it serves plain HTTP behind the existing trusted reverse proxy. Coin specifications may opt in with `ports.blockbook_insight` and `blockbook.insight_binding_template`. Validate port/template pairing and use an explicit bind host; document loopback as the default example. Do not enable any production coin definition or assume a port is free. Preserve `9876` as an operator-selected example only.

### Supported HTTP contract

Implement the five existing adapter route families, including its optional address-query and history path-offset forms where unambiguous:

- `GET /api/status`: `{info:{blocks,version,protocolversion,difficulty,errors}}`.
- `GET /api/addr/:address/utxo`: array with address, txid, vout, exact coin amount, satoshis, height, confirmations, and coinbase where known.
- `GET /api/addr/:address[/:noTxList]`: balance summary with explicit zeros, `balanceSat` string for adapter compatibility, numeric other satoshi fields, and original `txApperances`/`unconfirmedTxApperances` spellings. Like the current adapter, this profile does not include a transaction list. Use `AccountDetailsTokens` so pending balance is computed.
- `GET /api/addrs/:address/txs?from=0&to=50`: one address, exclusive-end offsets, response `{totalItems,from,to,items}`. Default window 0..50. Require integer `0 <= from <= to <= 1000`; reject requests beyond this documented initial bound. Fetch a txid prefix, deduplicate, slice, then enrich only returned IDs. Include pending transactions according to the worker's ordering. Return an empty array rather than clamping to the last page when the offset is beyond available history. Return actual `to = from + len(items)`. Comma-separated multiple addresses are explicitly unsupported with a 400 error.
- `POST /api/tx/send`: JSON `{rawtx}` to `{txid}`. Limit body to 8 MiB including chunked input; validate a nonempty even-length hexadecimal string, reject malformed/trailing JSON and non-string input. Preserve the daemon as transaction acceptance authority.

Dedicated DTOs preserve exact amounts using integer/decimal formatting and JSON number tokens, never float64 arithmetic. Spending satoshis outside JavaScript's safe integer range must fail explicitly. Handle nil amounts, coinbase, missing input addresses, zero values, and OP_RETURN from actual script bytes; do not fabricate script ASM from address text. Preserve history fields consumed by wallet display/export: txid, time, confirmations, fees, input addr/value/valueSat, output n/value/scriptPubKey.addresses/hex/asm where known, block height/hash, transaction hex where available.

The history bound limits prefix size requested and full transaction enrichment; legacy workers still scan all pending address transactions, and the UTXO method returns the full set. Document this residual backend-work risk. Deduplicate IDs preserving first occurrence, while retaining totalItems as the worker counters observed during the request; counters and rows are not an atomic chain snapshot and may drift during confirmation/reorgs. A nonempty SystemInfo.Backend.BackendError is a generic 503 even if GetSystemInfo returned no Go error; never forward that field.

This is a documented compatibility profile, not all Insight or Socket.IO. History beyond the limit, multiple-address aggregation, special/shielded asset semantics, and event subscriptions require later contract work before those consumers migrate.

### Request safety and lifecycle

Return the existing `{status:"error",error:"..."}` shape with 400 for invalid input, 405 for wrong methods, 413 for oversized bodies, 503 while not ready, and generic 500 for unexpected backend errors. Do not expose raw RPC error URLs, credentials, or stack traces. Distinguish known daemon transaction rejection if possible without leaking backend messages. Set CORS for all responses and OPTIONS, matching the existing adapter's public cross-origin use. Include the same fork source URL in a Link response header with rel="source", including errors and OPTIONS, so API-only clients can discover corresponding source without an HTML listener. Unknown routes must return 404.

Bind synchronously so collisions fail startup. Set HTTP read-header/idle/write limits and allow at most 32 in-flight worker requests. Excess requests receive immediate 503 with Retry-After: 1; do not create a waiting queue. Mark the listener ready only when Blockbook's initial sync is complete; status may remain available while syncing. Use an owned atomic readiness flag rather than reading InternalState.InitialSync concurrently. With -sync, readiness follows both initial index and mempool initialization; without -sync, follow existing public-server startup readiness and document that this does not certify a current index. Register cleanup immediately after startup on every main return path. Track requests, including status, so shutdown drains them before closing the chain or DB; a net/http timeout alone is insufficient while existing worker methods cannot be cancelled. After an HTTP shutdown deadline, close connections but keep waiting for actual worker calls before allowing DB/chain closure. This may exceed the deadline if a legacy backend call stalls. Do not alter existing public API behavior or network bindings.

### Tests and qualification

Use httptest with a fake narrow worker for deterministic local tests of all route families, nil/zero/negative/one-satoshi values, JS-safe boundaries, coinbase/OP_RETURN, mempool/confirmed history boundaries and overlapping offsets, invalid routes/methods/CORS, body limits and invalid broadcast requests, sanitized errors, startup collisions, readiness, and shutdown with active handlers. Test real config rendering with feature absent/enabled/invalid pairs. Run server tests, build-tool tests, database tests, then the available local unit suite. Do not run production integration tests.

Release qualification remains distinct from implementation: consistent production DB clone, pinned build/library compatibility, recovery rehearsal, and client contract checks must precede a user-run canary. No permanent duplicate index is required. A rollback that crosses a write migration needs a restored snapshot; this patch must not introduce such a migration.
