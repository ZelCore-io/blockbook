# Fork Release Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task by task.

**Goal:** Establish protected maintenance/development release lines and forward-port approved features into reviewable current-master PRs.
**Architecture:** Master integration, older maintained release line, immutable release tags and per-deployment artifact pins. Four stacked development PRs (policy, upstream sync, branding, Insight) preserve independent review.
**Tech Stack:** Git/GitHub rulesets and Actions; existing Go1.25.4/RocksDB9.10 stack; no new runtime dependencies.
**Spec:** docs/superpowers/specs/2026-10-15-fork-release-management-design.md

## Global Constraints

- User approved design and requested GPT-6.1 Sol subagents; no nested agents.
- No Ansible/SSH, hosts, production connectivity, integration tests, deployments or master merges. Draft PRs only, all self-hosted jobs skipped.
- No fork-specific DB/parser/module/production coin edits. Reviewed upstream changes are explicitly isolated in Task2. Preserve newer environment/build/jemalloc and main signal/lifecycle behavior.
- Worktree /Volumes/MAC_EXTERNAL/repos/zelcore-blockbook-forward-20261015 begins at master44242e0d. Maintenance worktree /Volumes/MAC_EXTERNAL/repos/zelcore-blockbook-maintenance-20261015 begins at brandinga3bcc18a. Existing checkout/release evidence stay intact.
- Original feature contracts are recorded in the spec; copy reviewed implementation then adapt only newer interfaces.

### Task 1: Release policy, safe draft CI and reviewable protection settings

**Files:** modern .github/workflows/testing.yml; new docs/fork-release-policy.md; .github/repository-rules/branches.json and release-tags.json. Maintenance worktree: matching policy/rules files plus new .github/workflows/testing.yml. Own only these paths in both trees.
**Interfaces:** Produces job contexts Unit Tests and Lint for both branch lines and ruleset payloads for controller to apply. No remote writes by worker.

- [ ] Read current workflow and baseline old Makefiles. Preserve master job implementations and existing same-repository guard. Add PR types opened/synchronize/reopened/ready_for_review and draft guard to the three self-hosted jobs:
```yaml
if: ${{ github.event_name != 'pull_request' || (github.event.pull_request.head.repo.full_name == github.repository && !github.event.pull_request.draft) }}
```
- [ ] In maintenance workflow, only pull_request for release/zelcore-0.5 with same types, permissions contents:read, concurrency cancel superseded PRs, hosted Lint and same-repo nondraft Unit Tests using make test ARGS="-count=1". Use existing pinned checkout SHA 3d3c42e5aac5ba805825da76410c181273ba90b1. Unit runner labels match current self-hosted bb-dev-selfhosted. Lint checks JSON config parsing, shell syntax of build/docker/deb/build-deb.sh and patch whitespace; no broad baseline formatting rewrite. No push/dispatch or daemon jobs.
- [ ] Write policy covering monthly sync review, merge ancestry, minimal active release lines, tagged artifacts/build once promotion, per-node config/daemon pins, source publication, copied DB compatibility and honest current qualification limits. Preserve existing PR1.
- [ ] Produce ruleset payloads using current official API schema: active branch target refs/heads/master + refs/heads/release/*; PR one approval/stale dismissal/thread resolution; required Unit Tests and Lint with GitHub Actions integration15368; strict up-to-date checks; non_fast_forward/deletion; no bypass. Tag target refs/tags/zelcore-v* with update/deletion, no creation block. Use distinct stable rule names.
- [ ] Validate YAML/JSON, shell/JSON source checks against maintenance checkout, hosted pipeline script/lint checks against master, inspect triggers/draft guard for draft/same-repo/fork cases. No Actions dispatch. Commit each worktree separately and report both SHA ranges and exact tests.

### Task 2: Integrate pinned current upstream while preserving draft safety

**Files:** upstream44242e0d..b427738f changes in API token handling, Ethereum header hash, GC/heap metrics/Grafana and Dependabot CI filtering; .github/workflows/testing.yml conflict resolution only for our draft guard.
**Interfaces:** Produces sync/upstream-20261015 based on reviewed policy, merging upstream b427738f with ancestry preserved; later features start here.

- [ ] User steering explicitly requests very fresh master with rolling fresh-index rebuilds. Read upstream four-commit diff and immutable baseline verification report. Do not refetch a moving target during implementation.
- [ ] Create sync/upstream-20261015 from reviewed policy and run:
```sh
git merge --no-commit --no-ff b427738f
```
Resolve only expected workflow overlap: preserve draft and same-repository gating for every self-hosted job; retain upstream Dependabot exclusions for connectivity/integration; keep ready_for_review event and hosted lint unchanged.
- [ ] Verify feature code is not yet present and upstream source paths exactly match b427738f except documented policy/docs/workflow guard. Run static workflow truth table, Grafana render check/tests and applicable pipeline unit tests. Inherit passing immutable b427738f Go suite/build evidence because changed integration files are docs/CI only; no redundant broad suite.
- [ ] Commit merge with both ancestry parents, record upstream SHA, full diff scope and tests. No push/PR/host actions by worker.

### Task 3: Branding forward port

**Files:** corresponding branding files in a3bcc18a (templates/about, server source helper and current render tests, build metadata Makefiles/Dockerfile/deb script, docs/zelcore-release.md). Preserve current-master unrelated contents.
**Interfaces:** sourceLink(revision) returns full clean SHA URL or development fallback; TemplateData source fields used by Task4 API source header.

- [ ] On new feature/zelcore-branding branch stacked on reviewed upstream sync, inspect original patch:
```sh
git show a3bcc18a774d0122ff15832ee79763b34973a406 --format= --binary
```
- [ ] Apply relevant patch with manual conflict resolution. Preserve master GetInitialSync and TxIOCollapseThreshold, generic htmlTemplates, current large fixtures, BB_BUILD_ENV/BB_RPC_ENV and static jemalloc. Update only source metadata/branding values in existing fixtures. Do not import old release qualification claims as current-master evidence.
- [ ] Run original meaningful source helper/render tests with clean/dirty/unknown metadata and actual debug/production templates. Run release guard/build metadata dry runs and negative controls. Use supplied Go1.25.4 environment, not old Go1.23.7.
- [ ] Run affected normal server/API/db checks and native build; full suite may be reserved for combined candidate provided baseline passed. Record feature-only DB/parser/module invariance relative reviewed upstream b427738f; upstream changes are separately identified. Inspect rendered desktop/mobile UI if changed; original CSS/template behavior is preserved. Commit owned changes and record branch SHA.

### Task 4: Native Insight forward port

**Files:** corresponding native files in dda65ea2 plus root main lifecycle/test, current build/tools templates/tests, current service template, docs/config.md, docs/insight-compatibility.md, retained MIT notice.
**Interfaces:** Same narrow worker methods as old implementation; current main shutdown accepts shutdownSig <-chan os.Signal. Consume sourceLink from Task3.

- [ ] On feature/native-insight stacked on reviewed branding, reuse approved new server files/types/tests and notice; manually integrate existing main/config/tests/service. Existing worker contract remains compatible.
- [ ] Preserve signal fan-out; add Insight argument to waitForSignalAndShutdown while retaining dedicated shutdownSig parameter. Adapt lifecycle test to its own buffered signal channel; no global channel swap. Retain state-storage/index/mempool/healing joins and sync metrics.
- [ ] Append config tests to existing build/tools suite, isolating existing override environment and testing dev/prod. Service fragment preserves EnvironmentFile, jemalloc, backend dependency and pprof. No production coin config enabled.
- [ ] Run route/money/history/broadcast/readiness/source/admission/shutdown tests; meaningful negative controls for changed lifecycle/test integration. Run root/server/buildtools tests normal and race; full offline contrib/tests/run-unit-tests.sh and native binary build. No connectivity/integration tests. Update compatibility docs with current-master evidence and remaining qualifications. Commit and report.

### Task 5: Final review, remote branches and handoff

**Files:** docs/fork-forward-port-validation-2026-10-15.md plus local evidence and PR body files. Controller owns repository remote settings/publication; worker documents evidence before final review.
**Interfaces:** Consumes independently reviewed task commits; publishes maintenance + policy + branding + Insight + original stacked source branch; creates draft stacked PRs. No merges/tags/releases.

- [ ] Record source invariance, toolchain/tests, old-versus-new limitations, integrated upstream SHA and four-commit review, and source publication requirements. Final read-only review checks policy/CI/combined native integration and maintenance branch.
- [ ] Push new branches only after review and verify no branch push workflow can contact hosts. Create draft policy PR→master, upstream sync PR→policy, branding PR→sync, Insight PR→branding with accurate base scopes and later retarget instructions. Existing PR1 unchanged.
- [ ] Check resulting runs: hosted lint may run, self-hosted jobs skipped. Apply additive reviewed rulesets via gh api and read back exact conditions/rules. No fabricated status checks.
- [ ] Report PR URLs, maintenance branch, protected refs, actual checks and remaining Linux/prod DB qualification; local master stays unchanged.
