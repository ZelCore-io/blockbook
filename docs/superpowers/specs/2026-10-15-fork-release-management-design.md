# Fork release management and forward ports

User approved this branch/release design on 2026-10-15. This implements that approval without another design gate.

## Branch model

Keep master as the maintained development line, currently 44242e0d24d1b9a78ce77df05c9221f6789ba463. Establish release/zelcore-0.5 from the independently reviewed branding candidate a3bcc18a774d0122ff15832ee79763b34973a406; this is a maintenance candidate, not a production-certified release. Keep the existing stacked Insight branch available. User further approved a rolling fresh-index rebuild on one server at a time to reach current upstream-based master. Integrate observed upstream b427738f before feature ports, then port branding and Insight in separately reviewable stacked PRs. The 0.5 line is a temporary maintenance reference; actual rollback uses existing deployed binaries and their retained database snapshots. Check per-coin replica availability/disk capacity before reindex; a single-replica coin needs temporary capacity or a planned outage. Drain one replica, create a fresh index, verify height/hash, API/wallet behavior and resources, switch traffic only after acceptance, then repeat. This describes future user-run operations; no indexes are deleted here. No automatic merge or deployment. Do not permanently branch per coin. Ansible inventories should eventually pin release tag/source SHA, artifact digest, config revision and daemon version independently; this task does not modify Ansible.

Origin is ZelCore-io/blockbook; upstream is trezor/blockbook. Fetch upstream and record pending revisions; do not silently merge upstream into production. Monthly upstream review and prompt relevant security/correctness assessment are operating guidance, not scheduled automation. Sync via short-lived sync branches with merge ancestry preserved. Older release lines accept selected fixes. Fixes reach both master and affected maintenance lines.

## Review and protection

Require a PR with one approval, resolved review threads, stale approvals dismissed, successful Unit Tests and Lint checks from GitHub Actions, and prohibit force pushes/deletion on master and release/* branches. Do not require linear history because upstream merges preserve ancestry. Release tags matching zelcore-v* are immutable after creation; no release tag is created until qualification. Apply additive repository rules only after reading current settings; preserve pre-existing rules and PRs.

Current Testing workflow runs self-hosted unit/connectivity/integration jobs on PRs. Add draft gating to all three and ready_for_review to PR event types. Hosted lint remains available. All PRs created here remain drafts, preventing host access. Legacy release line receives a compatible Testing workflow with hosted Lint and draft-gated same-repository Unit Tests using its own make test. It has no deployment/connectivity/integration jobs and no push trigger. No self-hosted jobs are dispatched during this work.

## Feature contracts

Forward-port the already reviewed branding contract from a3bcc18a: Zelcore/docs/Flux/source links, attribution retained, full clean source metadata and clean release guards. Preserve newer templates, environment-aware build/config plumbing and jemalloc linkage.

Forward-port the reviewed Insight contract from dda65ea2ee6ddce23966c2804483b6337f3ad771: optional disabled-by-default native listener sharing one worker/DB; Bitcoin-type eight-decimal chains; same five route families and aliases; single-address exclusive-end 0 <= from <= to <= 1000, default0..50; exact money types, bounded8MiB broadcast, max32 inflight503 admission, readiness after initial index/mempool, source Link/CORS, resource drain before DB/daemon close with separate timeout. Preserve master signal fan-out and explicit shutdown signal argument, synchronized InitialSync, metrics and shutdown joins. No production coin enabled, no DB/parser/modules changed.

## Verification and publication

Native current-master toolchain uses pinned Go1.25.4, RocksDB9.10 and ZeroMQ4.3.5 in scratch, with platform limits recorded. Run baseline and candidate offline unit suites, affected race checks, binary build, real template rendering and service configuration tests. No Ansible/SSH, daemon/production connectivity, integration tests or deployment. Docker is unavailable; Linux packaging and copied-production-database/real-wallet qualification remain outstanding. Matching schema7 between old and new baseline is not compatibility proof.

Publish reviewed branches and draft PRs, verify hosted lint and skipped self-hosted jobs, then apply/read back protection rules. Publish old stacked source branch too so its exact commit source links are accessible, without marking it deployed or tagged. Preserve existing PR1 and unrelated add-clore/add-tls/zelcore branches.
