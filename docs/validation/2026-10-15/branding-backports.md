# Branding-only GRS backport applicability

Result: PASS on both recorded deployed bases.

Branding patch: a3bcc18a774d0122ff15832ee79763b34973a406 parent-to-commit binary diff only. No native Insight changes included.

- Older GRS: 9dfbb10c47ea8fb01491a2c69b5a1e86fba185e6
- Newer GRS: dcdb2bd25a36da6e5d90a148cc183692c4e14ef6

For each base, in a scratch shared object clone with a separate index: read-tree BASE; apply --cached --check branding-only.patch; apply --cached branding-only.patch; diff --cached --check BASE; write-tree. All commands exited zero. The staged index retains the full base tree plus exactly the branding commit changes. Per-base changed-file lists, diff stats, empty successful check logs and resulting tree IDs are preserved here.

No working-tree checkout, source repository mutation, commit, host access, deployment or additional test suite occurred. Shared alternate object storage is read-only with all newly written objects in the scratch clone. Applicability is not runtime qualification; each release still needs tests, correct full release SHA metadata and its own qualification. Existing backend-version config differences remain inherited from the chosen deployed base.
