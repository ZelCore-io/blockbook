# Zelcore Blockbook fork release policy

`master` is the maintained development and integration line of
[ZelCore-io/blockbook](https://github.com/ZelCore-io/blockbook).
[trezor/blockbook](https://github.com/trezor/blockbook) remains the upstream
project; retain its attribution and license notices. Keep only release lines
that support actual deployments or an explicit qualification effort. Start
`release/zelcore-0.5` from the reviewed branding candidate
`a3bcc18a774d0122ff15832ee79763b34973a406`. That ancestry identifies a maintenance
candidate and temporary maintenance reference while the fresh development line
is qualified, not a production-qualified rollback release. The actual rollback
is the currently deployed artifact and its compatible database snapshot. Retire
the 0.5 line when that fallback/reference is no longer needed. Do not create
permanent branches per coin.

## Upstream and maintenance changes

Review upstream at least monthly, and assess relevant security and correctness
fixes promptly. Record the reviewed upstream revision, pending changes, affected
chains, test evidence and disposition. This is an operating cadence, not an
automated upstream merge or deployment schedule.

The observed upstream baseline on 2026-10-15 is
`b427738facd3a8c4cf3986e6e11d868a214deeea`, four commits ahead of fork master
`44242e0d24d1b9a78ce77df05c9221f6789ba463`. Review and prepare that upstream
sync before stacking the branding and Insight forward ports. Record the exact
reviewed SHA again if upstream advances; a moving branch name is not a release
pin. This observation does not authorize a merge.

Use short-lived `sync/*` branches and merge upstream through reviewed PRs to
`master`, preserving merge ancestry. Do not squash or rebase away upstream sync
ancestry. Apply selected fixes to affected older release lines through their own
PRs and record the original commit reference and any adaptation. Ensure a fix
needed on both lines reaches both; a maintenance-only fix does not imply master
is covered. Retire older release lines when deployments no longer need them.

Keep branding and native Insight forward ports independently reviewable. Preserve
the existing Clore/TLS PR #1, its branches and its review scope; release management
work does not supersede or merge it.

## Review and CI

Protect `master` and `release/*` with PRs, at least one approval, dismissal of
stale approvals, resolved review threads, and successful `Unit Tests` and `Lint`
checks from the GitHub Actions app (integration ID `15368`). Require checks
against the current target branch. Prohibit force pushes and branch deletion,
with no bypass actors. Allow merge commits so upstream ancestry remains intact.
The two JSON payloads in `.github/repository-rules/` describe the intended rules;
committing them does not apply protection settings.

The current development workflow runs `Unit Tests` on ephemeral GitHub-hosted
Ubuntu 24.04 runners for `master`/`develop` pushes and non-draft PRs, including
fork PRs. It uses the existing Docker `make test` pipeline with explicit
`BASE_IMAGE=ubuntu:24.04 PORTABLE=1` and a 90-minute timeout for a fresh RocksDB
build. The hosted unit job uses no repository secrets and has only
`contents: read` permissions, with pinned checkout and credential persistence
disabled. Hosted lint also runs for draft and fork PRs. PR events include
`opened`, `synchronize`, `reopened` and `ready_for_review`, so marking a draft
ready can schedule unit tests. Returning a PR to draft does not cancel an
already-running hosted job.

Live connectivity and RPC/sync integration tests remain on the self-hosted
runner and run only through `workflow_dispatch` with the boolean
`run_live_tests` input explicitly enabled (default `false`). A manual run first
runs the hosted unit job; connectivity then gates integration. PR and push
events never schedule these live jobs. Required checks and branch protections
remain unchanged; passing hosted checks alone does not establish live backend
compatibility or release qualification.

The `release/zelcore-0.5` workflow runs only on PRs targeting that branch. Its
hosted lint parses JSON configuration, checks package script shell syntax and
checks the PR patch for whitespace errors. Its same-repository, non-draft unit
job runs the legacy line's `make test ARGS="-count=1"`. It has no push, dispatch,
connectivity, integration or deployment jobs. Fork unit jobs are skipped rather
than executed on the shared self-hosted runner. A skipped required job is not
unit-test evidence: review and test a trusted same-repository candidate before
merging a fork contribution. Superseded PR runs are cancelled on both lines.

## Release identity and promotion

Create a `zelcore-v*` tag only after review and qualification of the exact source
revision. Matching tags are immutable after creation: prohibit updates and
deletion with no bypass actors, while permitting initial creation. If a release
needs correction, create a new qualified revision and tag. The rule payloads use
the [GitHub repository rulesets REST schema](https://docs.github.com/en/rest/repos/rules#create-a-repository-ruleset).
Before applying settings, read existing repository and inherited rules, preserve
unrelated settings, then apply the reviewed rules additively and read them back.

Build release artifacts once from a complete clean checkout of the exact tagged
commit. Record the full source SHA, tag, toolchain and native dependency versions,
artifact digests, configuration revision and required daemon versions. Publish
the exact corresponding source and retained license notices with accessible
commit links; publish the original reviewed stacked source branch too when its
commit links are referenced. Do not label a dirty or abbreviated source revision
as release provenance.

Promote the same qualified artifact digest through validation and production;
do not rebuild it independently per node or environment. Keep per-node pins for
the source/tag and artifact digest, configuration revision and daemon version
separate. Review configuration and daemon changes independently from explorer
code. Future Ansible inventories should express those pins; this policy does not
modify inventory or deploy anything. Keep the prior artifact and configuration
pins and a compatible database snapshot available for a tested rollback.

## Qualification and current limits

The primary qualification route is a rolling rebuild with a fresh index on the
reviewed current upstream development candidate. Before starting, check disk
space, synchronization duration and serving capacity, including room to retain
the old database snapshot alongside the new index. For each coin, keep a healthy
replica serving while one node drains and rebuilds in a new database directory.
Retain the exact deployed artifact, configuration/daemon pins and a compatible
old database snapshot; the unqualified branding maintenance branch is not a
replacement for that rollback evidence.

Validate Linux packaging and startup, chain-specific daemon RPC compatibility,
complete synchronization, mempool health, wallet behavior and required API
responses against the existing serving replica
before moving traffic to the rebuilt node. Record explicit cutover and rollback
criteria and observe the replacement serving correctly before proceeding to the
next node. A single-replica coin needs temporary serving capacity or a planned
outage; do not assume a rolling rebuild can preserve availability without it.
No node is drained, indexed or switched by this policy change.

A fresh database avoids relying on old-index reuse. If reusing an existing index
is later proposed, first test an isolated copy of the relevant production
database and record migration behavior, indexing, balances/history, shutdown and
restart results. Matching schema 7 on the two baselines does not establish
compatibility: storage behavior also changes. Never open the live database from
a second process. Keep a verified backup and use the old compatible snapshot
for rollback if a migration or write changes the new database. Record source,
artifact, configuration and daemon pins and database path/snapshot identity for
every qualification and cutover.

The existing branding and Insight candidates and the current-master forward
ports remain qualification candidates. Local offline unit tests, race checks,
native builds and template/configuration checks provide scoped evidence only.
They do not establish Linux package correctness, production database
compatibility, live daemon connectivity, real-wallet compatibility or deployment
readiness. Docker/Linux packaging, rolling fresh-index and real-wallet
qualification remain outstanding; copied-production-database compatibility is
also unproven and is required only if old-index reuse is chosen. No production
coin enables the optional Insight listener as part of this work. No release tag, deployment, automatic
merge or host operation is authorized by this policy file.
