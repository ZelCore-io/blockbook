# Zelcore Blockbook Fork Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a separately reviewable branding release and native Go Insight compatibility implementation on the deployed Blockbook baseline.

**Architecture:** One Blockbook process owns its database; its existing UI gains ecosystem links and an exact source link. An optional independent HTTP listener maps the supported Insight profile onto the same worker.

**Tech Stack:** Existing Go 1.23 module, HTML templates, RocksDB 9.10.0, standard-library HTTP/JSON, existing build templates. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-10-15-blockbook-fork-design.md`

## Global Constraints

- Base: `b14641d979241b93108025f2a72b4517ab4db260`; no upstream upgrade, database/parser/index changes, dependency-version changes, or backend configuration changes.
- No Ansible, SSH, production connectivity tests, host changes, deployments, master merges, or release publication.
- All implementation/review agents use `gpt-6.1-sol`; no nested subagents.
- Preserve AGPL and upstream attribution; reused MIT adapter logic retains its notice.
- Branding and Insight remain independently reviewable and separately committed.
- Insight is disabled by default and enabled only for Bitcoin-type eight-decimal chains.
- Supported history is single-address, exclusive-end `0 <= from <= to <= 1000`, default 0..50; JSON broadcast bodies are limited to 8 MiB.
- Exact integer/decimal monetary formatting; no float64 amount arithmetic; fail on unsafe spending integers.
- Existing adapter summary preserves string `balanceSat`, numeric other satoshi fields, explicit zeros, no transaction list, original appearance-field spellings.
- No UI redesign, dynamic feeds, campaign/dashboard implementation, or shared TS frontend in these chunks.

---

### Task 1: Branding release and accurate build-source metadata

**Files:**
- Modify `static/templates/base.html`, `static/templates/index.html`, `api/embed/about`.
- Modify `server/public.go` minimally for source metadata and `server/public_test.go` rendered expectations; create a focused source-helper/test file only if useful.
- Modify `Makefile`, `build/docker/bin/Makefile`, `build/docker/bin/Dockerfile`, and `build/docker/deb/build-deb.sh` only where necessary to propagate revision metadata and remove upstream preload.
- Create `docs/zelcore-release.md` with build, source-publication and manual qualification instructions.

**Interfaces:**
- Consumes `common.GetVersionInfo().GitCommit` and existing TemplateData.
- Produces a source URL on all rendered explorer pages and full revision metadata through supported release builds. Task 2 must not modify these semantics.

- [ ] Read the spec, current AGENTS, and existing render tests. Establish baseline with local test toolchain supplied by controller; record pre-existing limitations.
- [ ] Update meaningful source-link tests before helper changes; update the existing rendering fixture for intentional template copy changes. Cover a clean 40-hex revision and unknown/dirty development identifiers. A clean build's source target must be `https://github.com/ZelCore-io/blockbook/tree/` plus that exact revision; development builds must not render a broken commit path.
- [ ] Implement the header/footer/about changes using existing Bootstrap classes. Keep terms and attribution, change status commit URL to the fork, and avoid external scripts or assets.
- [ ] Remove Docker's unpinned upstream module preload. Allow explicit `GITCOMMIT` propagation through binary/Debian builds. Ordinary local builds may be marked dirty; release commands must reject dirty tracked or untracked source and use `git rev-parse HEAD`. Test command construction with `make -n` and shell syntax checks; do not run backend package targets.
- [ ] Run available server tests in debug and production template modes, then inspect a rendered local desktop/mobile preview. Document exact commands/results and unavailable checks. Commit only owned files once covering checks pass, or report a concrete toolchain blocker without claiming success.

### Task 2: Native Insight handlers, listener lifecycle and coin-spec integration

**Files:**
- Create `server/insight.go`, `server/insight_types.go`, `server/insight_test.go`; split a focused helper if it improves clarity.
- Modify `blockbook.go`, `build/tools/templates.go`, `build/templates/blockbook/debian/service`, `build/tools/templates_test.go` (create tests if absent), `docs/config.md`.
- Create `docs/insight-compatibility.md` and a retained MIT notice for reused adapter material. Do not enable production coin configurations.

**Interfaces:**
- Consumes unchanged deployed worker methods via a narrow interface:
```go
type insightWorker interface {
    GetSystemInfo(bool) (*api.SystemInfo, error)
    GetAddress(string, int, int, api.AccountDetails, *api.AddressFilter, string) (*api.Address, error)
    GetAddressUtxo(string, bool) (api.Utxos, error)
    GetTransaction(string, bool, bool) (*api.Tx, error)
}
```
- Inject broadcast as `func(string) (string, error)`; the deployed daemon method takes one argument.
- Produces an optional `-insight=<host>:<port>` listener, constructor/start readiness/shutdown wiring in main, and optional `ports.blockbook_insight` + `blockbook.insight_binding_template` configuration. Exact exported constructor/lifecycle signatures should follow existing server conventions and stay in this task.

- [ ] Read the full supported contract and lifecycle requirements in the spec. Before implementation, create table-driven httptest contracts against the interface. Example expected fixture behavior: a one-satoshi UTXO encodes `"amount":0.00000001,"satoshis":1`; a summary with pending -1 satoshi encodes numeric `unconfirmedBalance:-0.00000001`; history IDs `[pending,a,b,c]`, from=1,to=3 returns `[a,b]`, totalItems=4,to=3. Verify initial failures.
- [ ] Implement exact monetary DTOs, summary/status/UTXO mapping, tx conversion, and prefix-based history slicing. Use AccountDetailsTokens for balances and AccountDetailsTxidHistory for history prefix; slice before fetching full transactions. Refuse multi-address and unsupported windows. Preserve coinbase and script-derived OP_RETURN.
- [ ] Implement bounded JSON broadcast with success `{txid}` and safe errors. Cover empty/nonstring/odd/nonhex/malformed/trailing bodies and 8 MiB+1 bytes with or without Content-Length. Never forward raw backend errors containing credentials.
- [ ] Add routing aliases already supported by the Node adapter, method checks, unknown-route handling, CORS/OPTIONS, readiness and concurrency limits. Test a bound port collision and an active request during shutdown; ensure worker/DB access is finished before main closes resources. Start disabled and reject unsupported chain types/decimals.
- [ ] Wire CLI and main startup/shutdown. Add optional specification fields and template rendering; reject partial configuration. Example enabled values are `ports.blockbook_insight:9876` and `blockbook.insight_binding_template:"127.0.0.1:{{.Ports.BlockbookInsight}}"`; generated ExecStart includes `-insight=127.0.0.1:9876`. Absent fields must not change existing ExecStart.
- [ ] Test actual config/service rendering for disabled/enabled/invalid pair, then run local server/build-tool/database tests and the available unit suite. Document exact profile bounds, differences from full Insight, no second DB/index, and manual production qualification steps. Commit owned code and tests after verification.

### Task 3: Release qualification record and independent review

**Files:**
- Update `docs/zelcore-release.md` and `docs/insight-compatibility.md` with actual evidence, qualified build command and remaining deployment checks.
- Create `docs/zelcore-validation-2026-10-15.md` with baseline/candidate hashes, tests, screenshots if available, and backport application evidence.

**Interfaces:**
- Consumes Task 1 branding commit(s), Task 2 optional-listener commit(s), and actual toolchain/test results.
- Produces a reviewable local branch and explicitly bounded release readiness statement; no deployment or public release.

- [ ] Verify `git diff b14641d9 -- db bchain go.mod go.sum` is empty and DB schema remains 7. This is a source invariance check, not a substitute for a copied production DB rehearsal.
- [ ] Check branding patch applicability against both recorded GRS revisions in scratch worktrees without altering those deployments. Record whether the patch applies and any build metadata differences; do not create a massive downgrade PR against current master.
- [ ] Collect final server/template/config/build tests and local preview evidence. Resolve review findings through the original implementer, with covering tests rerun after fixes.
- [ ] Document that production snapshot reopen/catch-up, daemon integration, source publication, port allocation and user-run canary are outstanding. Preserve a branding-only branch and a stacked Insight branch. Finish with exact paths, commits, checks, and limitations.
