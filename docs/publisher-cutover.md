# Env publisher and UI retirement

Env 0.3.1 completed verified monorepo publication and its obsolete npm binding is retired. Publishing is disabled again by the absent `ENV_PUBLISHER_ENABLED` variable. Tokens and UI are private workspace packages; their monorepo publisher and prepared trust entries are removed. [UI retirement](ui-retirement.md) supersedes the earlier UI activation plan. Old repositories remain unarchived, deployments unchanged, and Cache #94 excluded.

## Env release controls

The installed `.github/workflows/release-env.yml` accepts only `env/vX.Y.Z` tag pushes while explicitly enabled. Protected `npm-env` requires manual approval, permits the sole maintainer's approved self-review policy and disallows administrator bypass. Separate tag rules permit maintainer creation and prevent tag update/deletion without bypass. Existing main protection remains unchanged.

The exact public package set is only `@astilba/env`, with monorepo metadata at `packages/env`. Release identity requires a canonical stable version and a clean tagged checkout at current main. The complete package matrix verifies and transfers one exact archive; publication rechecks current-main identity after human approval. Only publish receives OIDC write permission. Publishing uses provenance, disabled lifecycle scripts and no long-lived npm token.

Registry verification binds archive bytes, exact versions, a clean consumer, cryptographic npm signatures and both signed attestation kinds to independently verified artifacts, the exact repository/workflow/tag/commit and GitHub-hosted builder. Protected environment and authenticated trust readback independently establish environment identity. The GitHub release adapter rereads both exact archive/manifest assets. The existing Env workflow, runtime pins and all acceptance gates remain unchanged.

## UI is not a publisher

`cutover.json` marks UI `retired-workspace-only` and has no UI enable variable. Its old namespace and package identities remain solely for offline archive/attestation readers; publisher identity and release packing reject UI even with synthetic public metadata. No UI workflow is installed, and prepared npm trust lists only Env. UI build, packaging, source, browser, accessibility and screenshot checks remain active.

Existing `npm-ui` controls and immutable tag rules are retained, not activated or deleted. The old UI release workflow is disabled and both old npm connections are removed. UI/Tokens registry packages were permanently unpublished after the separately verified legacy Docs archive bridge. Historical tags/releases remain intact; do not recreate removed versions or publishing relationships. [Deployment preparation](deployment-cutover.md) leaves production with the old repositories and does not enable any publisher or deployment.

Run `pnpm verify:publisher`, `pnpm verify:boundaries` and full `pnpm verify`. Actionlint and Zizmor retain the existing root workflow scans. Never broaden a release namespace to the root, Cache, UI or an unrelated product. Remove the Env enable variable after any approved release window; do not move, delete, recreate or reuse existing tags or versions.
