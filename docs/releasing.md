# Releasing

Only Env remains publishable from this monorepo. Its verified 0.3.1 release is complete and `ENV_PUBLISHER_ENABLED` remains absent. See [publisher controls](publisher-cutover.md) for its protected, exact-artifact release process; publication requires separate maintainer approval.

Tokens and UI are private workspace packages, not new npm release candidates. Their monorepo publisher is removed; do not create UI release tags or reintroduce publishing configuration. [UI retirement](ui-retirement.md) preserves existing registry versions and the old deployment path pending separately approved permanent removal. Old repositories, historical tags/releases and all deployments remain unchanged.
