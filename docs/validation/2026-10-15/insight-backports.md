# Cumulative release GRS backport applicability

Result: PASS on both recorded deployed bases.

Patch: complete binary diff from b14641d979241b93108025f2a72b4517ab4db260 to dda65ea2ee6ddce23966c2804483b6337f3ad771, covering branding and native Insight plus the two intervening design/scope documentation commits. This deliberately preserves the native commit's documentation prerequisites; it does not claim a cherry-pick of only two implementation commits.

- Older GRS: 9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6
- Newer GRS: dcdb2bd25a36da6e5d90a148cc183692c4e14ef6

For each base, in a scratch shared object clone with a separate index: read-tree BASE; apply --cached --check cumulative-release.patch; apply --cached cumulative-release.patch; diff --cached --check BASE; write-tree. All commands exited zero. Per-base changed-file lists, diff stats, empty successful check logs and resulting tree IDs are preserved here. Included commits are listed in included-commits.txt.

No source working-tree checkout or mutation, commit, host access, deployment, config edit or additional test suite occurred. Existing backend-version config differences remain inherited from the chosen deployed base. Shared alternate object storage is read-only, with newly written objects local to the scratch clone.

Applicability is not runtime/API qualification. Per-base builds/tests, actual BTG/GRS snapshot rehearsal, exact release SHA metadata, Linux runtime qualification and measured deployment acceptance remain separate requirements.
