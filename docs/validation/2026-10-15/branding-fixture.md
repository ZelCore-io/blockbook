# Offline database fixture rehearsal

Result: PASS (all four test processes exited successfully).

- Baseline source: b14641d979241b93108025f2a72b4517ab4db260
- Candidate source: a3bcc18a774d0122ff15832ee79763b34973a406
- Native platform: Darwin arm64; Go 1.23.7; RocksDB 9.10.0; ZeroMQ 4.3.5.
- Baseline created fixture block 225493 and closed gracefully.
- Candidate reopened a copy, checked fixture columns, connected block 225494, checked updated columns and closed.
- Baseline reopened a candidate-written copy, checked columns, disconnected 225494, checked restored columns and closed.
- Candidate reopened the rollback copy and checked restored block 225493 state.

The fixture helper checks the height, address, transaction-address, balance/UTXO, and block-transaction indexes against expected bytes. All processes also check the exact expected tip hash and a gracefully closed persisted internal state. Schema version is checked by LoadInternalState.

Scope: upstream Bitcoin testnet fixtures only, using small temporary databases and a common native library build. This does not qualify actual BTG/GRS production database snapshots, Linux runtime libraries, network catch-up, resource capacity or live rollback. No production snapshot was available. No network integration suite was run.

Logs: 01-baseline-create.log, 02-candidate-reuse-advance.log, 03-baseline-read-rollback.log, 04-candidate-reopen-rollback.log. Harness: /tmp/blockbook-toolchain-20261015/release_fixture_test.go. All DBs were copied only after the preceding process exited.
