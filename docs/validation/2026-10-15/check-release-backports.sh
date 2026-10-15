#!/usr/bin/env bash
set -euo pipefail
root=/tmp/blockbook-toolchain-20261015
repo=${1:?usage: check-release-backports.sh REPOSITORY CANDIDATE_REVISION}
revision=${2:?usage: check-release-backports.sh REPOSITORY CANDIDATE_REVISION}
candidate=$(git -C "$repo" rev-parse --verify "$revision^{commit}")
baseline=b14641d979241b93108025f2a72b4517ab4db260
git -C "$repo" merge-base --is-ancestor "$baseline" "$candidate"
out=$(mktemp -d "$root/release-backports-${candidate:0:12}-XXXXXX")
printf '%s\n' "$out"
git -C "$repo" diff --binary "$baseline" "$candidate" > "$out/cumulative-release.patch"
git -C "$repo" log --format='%H %s' "$baseline..$candidate" > "$out/included-commits.txt"
git clone --shared --no-checkout --no-hardlinks "$repo" "$out/object-repo" > "$out/clone.log" 2>&1
for base in 9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6 dcdb2bd25a36da6e5d90a148cc183692c4e14ef6; do
  export GIT_INDEX_FILE="$out/$base.index"
  git -C "$out/object-repo" read-tree "$base"
  git -C "$out/object-repo" apply --cached --check "$out/cumulative-release.patch" > "$out/$base.check.log" 2>&1
  git -C "$out/object-repo" apply --cached "$out/cumulative-release.patch" > "$out/$base.apply.log" 2>&1
  git -C "$out/object-repo" diff --cached --check "$base" > "$out/$base.whitespace.log" 2>&1
  git -C "$out/object-repo" diff --cached --stat "$base" > "$out/$base.stat.txt"
  git -C "$out/object-repo" diff --cached --name-status "$base" > "$out/$base.files.txt"
  git -C "$out/object-repo" write-tree > "$out/$base.result-tree.txt"
done
unset GIT_INDEX_FILE
cat > "$out/report.md" <<EOF
# Cumulative release GRS backport applicability

Result: PASS on both recorded deployed bases.

Patch: complete binary diff from $baseline to $candidate, covering branding and native Insight plus the two intervening design/scope documentation commits. This deliberately preserves the native commit's documentation prerequisites; it does not claim a cherry-pick of only two implementation commits.

- Older GRS: 9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6
- Newer GRS: dcdb2bd25a36da6e5d90a148cc183692c4e14ef6

For each base, in a scratch shared object clone with a separate index: read-tree BASE; apply --cached --check cumulative-release.patch; apply --cached cumulative-release.patch; diff --cached --check BASE; write-tree. All commands exited zero. Per-base changed-file lists, diff stats, empty successful check logs and resulting tree IDs are preserved here. Included commits are listed in included-commits.txt.

No source working-tree checkout or mutation, commit, host access, deployment, config edit or additional test suite occurred. Existing backend-version config differences remain inherited from the chosen deployed base. Shared alternate object storage is read-only, with newly written objects local to the scratch clone.

Applicability is not runtime/API qualification. Per-base builds/tests, actual BTG/GRS snapshot rehearsal, exact release SHA metadata, Linux runtime qualification and measured deployment acceptance remain separate requirements.
EOF
cat "$out/report.md"
