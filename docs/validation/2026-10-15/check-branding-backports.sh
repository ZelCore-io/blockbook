#!/usr/bin/env bash
set -euo pipefail
root=/tmp/blockbook-toolchain-20261015
repo=/tmp/zelcore-blockbook-20261015
branding=a3bcc18a774d0122ff15832ee79763b34973a406
out=$(mktemp -d "$root/branding-backports-a3bcc18a-XXXXXX")
printf '%s\n' "$out"
git -C "$repo" diff --binary "$branding^" "$branding" > "$out/branding-only.patch"
git clone --shared --no-checkout --no-hardlinks "$repo" "$out/object-repo" > "$out/clone.log" 2>&1
for base in 9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6 dcdb2bd25a36da6e5d90a148cc183692c4e14ef6; do
  export GIT_INDEX_FILE="$out/$base.index"
  git -C "$out/object-repo" read-tree "$base"
  git -C "$out/object-repo" apply --cached --check "$out/branding-only.patch" > "$out/$base.check.log" 2>&1
  git -C "$out/object-repo" apply --cached "$out/branding-only.patch" > "$out/$base.apply.log" 2>&1
  git -C "$out/object-repo" diff --cached --check "$base" > "$out/$base.whitespace.log" 2>&1
  git -C "$out/object-repo" diff --cached --stat "$base" > "$out/$base.stat.txt"
  git -C "$out/object-repo" diff --cached --name-status "$base" > "$out/$base.files.txt"
  git -C "$out/object-repo" write-tree > "$out/$base.result-tree.txt"
done
unset GIT_INDEX_FILE
cat > "$out/report.md" <<EOF
# Branding-only GRS backport applicability

Result: PASS on both recorded deployed bases.

Branding patch: $branding parent-to-commit binary diff only. No native Insight changes included.

- Older GRS: 9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6
- Newer GRS: dcdb2bd25a36da6e5d90a148cc183692c4e14ef6

For each base, in a scratch shared object clone with a separate index: read-tree BASE; apply --cached --check branding-only.patch; apply --cached branding-only.patch; diff --cached --check BASE; write-tree. All commands exited zero. The staged index retains the full base tree plus exactly the branding commit changes. Per-base changed-file lists, diff stats, empty successful check logs and resulting tree IDs are preserved here.

No working-tree checkout, source repository mutation, commit, host access, deployment or additional test suite occurred. Shared alternate object storage is read-only with all newly written objects in the scratch clone. Applicability is not runtime qualification; each release still needs tests, correct full release SHA metadata and its own qualification. Existing backend-version config differences remain inherited from the chosen deployed base.
EOF
cat "$out/report.md"
