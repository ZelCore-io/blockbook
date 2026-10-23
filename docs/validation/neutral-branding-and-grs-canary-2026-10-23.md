# Neutral explorer branding and GRS canary validation

The common HTML template now uses `<CoinLabel> Explorer` in the title, description and navigation brand. Operator Zelcore and documentation links, Powered by Flux, source link and SatoshiLabs credit remain in the footer. No JavaScript or TypeScript changes.

## Local rendering

`go test static/templates/base_test.go` passes. This standard-library-only test executes the real base template for multiple coin labels (including HTML metacharacters), with the internal explorer enabled and disabled. It checks title, metadata, header, escaped output and retained footer credits/links.

The required `contrib/tests/run-unit-tests.sh` and `contrib/tests/run-integration-tests.sh -run 'TestIntegration/.*/connectivity'` both stop at system preflight: `CGO_CFLAGS` and `CGO_LDFLAGS` are not exported. The full repository suites did not run; the standalone render test does not establish Go server/build or C-library compatibility. `git diff --check` passes.

## Read-only deployed canary

Run `python3 contrib/scripts/check_grs_canary.py` from the repository root (Python 3 and curl). This deliberately pins the GRS02 revision to c5ade771; update the expectation for a future canary. The script makes ten sequential HTTPS GET requests, each with a 30-second timeout. It compares stable block/transaction fields rather than changing confirmation counts, and compares the full confirmed UTXO arrays. A chain tip change during requests can cause a benign UTXO confirmation mismatch; rerun and inspect before treating that as a regression.

The recorded successful run is [grs-canary-2026-10-23.json](grs-canary-2026-10-23.json), including UTC time, complete status responses and sample identifiers. GRS02 (`https://blockbook.grs-2.zelcore.io`) reports v0.6.0, revision c5ade771de895035480adf0f411e6250ff243183; GRS01 (`https://blockbook.grs-1.zelcore.io`) reports v0.5.0, revision 9dfbb10c. Both report index and mempool synced, initial sync false, matching chain tips. The script checked a block ten below the lower tip, its first transaction, the first address-bearing output's address totals, and confirmed UTXOs. All compared fields matched, including 519 confirmed UTXOs in this sample. Address totals compare address, balance, totalReceived, totalSent and txs; optional unconfirmed fields are excluded because the older API omits zero-valued fields.

Limits: one chain, one point-in-time sample, one confirmed block/transaction/address; the transaction selected is coinbase. This is not comprehensive compatibility, websocket, reorg, broadcast, mempool transaction, fee estimation, performance or load validation. The deployed canary predates this branding change, so API success does not verify deployment of the new template. No Ansible, deployment, host changes, node RPC or transaction broadcasts were performed. The first urllib attempt received HTTP 403; curl succeeds, so the reproducible script uses curl without bypassing TLS verification.
