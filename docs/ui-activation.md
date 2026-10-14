# UI publisher activation preparation

`release-ui.yml` is installed but disabled by the absent `UI_PUBLISHER_ENABLED` variable. Both packages' repository, homepage and issues metadata identifies `astilbahq/astilba` with exact `packages/tokens` and `packages/ui` directories. Source versions remain coordinated at **0.6.0**; exports, dependencies, peer ranges, CSS, components, lockfiles and application registry pins are unchanged. This is publisher preparation, not publication or deployment approval.

Env 0.3.1 completed verified monorepo publication, its old npm binding is retired and Env publishing is disabled again. UI still publishes from the original repository. On 14 October 2026, live registry history confirms both UI packages already contain 0.6.0 with old-repository metadata. Do not republish it: select a new unused coordinated version in a separate release PR. Old repositories remain unarchived; production deployments and private Cache #94 remain outside this work.

## Installed release contract

- Only a published, non-prerelease GitHub release with a canonical `ui/vX.Y.Z` tag can enter, and only while `UI_PUBLISHER_ENABLED=true`. There is no manual dispatch, tag-push trigger, cross-product fallback or unconditional downstream job.
- The tag must identify verified **current main** and both matching package versions. Complete UI source, package, Astro, browser/accessibility and dry-run verification runs before packing tokens and UI exactly once.
- Transfer binds commit/tree, exact files, entry modes and hashes to a manifest digest supplied independently by the verification job. Protected `npm-ui` rechecks current-main identity after approval and publishes exact tokens then UI archives with provenance and lifecycle scripts disabled.
- Only publish receives OIDC permission. GitHub-hosted runners, SHA-pinned Actions and pinned Node/npm/pnpm are unchanged. No long-lived publishing token or production credential is added.
- Registry acceptance checks both archive hashes, exact versions, a clean consumer, npm signatures and both signed attestations against verified archives and exact monorepo/workflow/tag/commit. The 40-minute timeout covers two package polls and the attestation poll; preparation does not prove live UI OIDC acceptance.

## Separate approval before first publication

1. Merge only after current-head checks and both eligible Codex/CodeRabbit reviews are clear. Keep both enable variables absent. Prepare a separate release PR for a new unused coordinated version, then recheck current main and both version histories before publication. This PR does not bump versions or create a release/tag; 0.6.0 is already used.
2. Configure and read back protected `npm-ui` with manual maintainer approval, no administrator bypass and tag-only `ui/v*` access. Approve its self-review policy explicitly; Env's sole-maintainer decision does not silently configure UI. Add separate maintainer-only creation and no-bypass update/deletion rules for `refs/tags/ui/v*`. This PR creates none of those settings.
3. An npm owner with write access to **both `@astilba/tokens` and `@astilba/ui`** and account 2FA reads each package's complete existing connections and publishing policy. No new access token is needed. Keep IDs and account evidence private; never copy credentials into the repository.
4. Within an explicitly approved first-release window, add GitHub `astilbahq/astilba`, filename `release-ui.yml`, environment `npm-ui` alongside each package's old binding. Request direct publish without dist-tag management, record npm's automatic staging permission, and verify effective readback and restrictive token policy. Preserve old connections until separately approved retirement.
5. With publication approval, pause only the old UI release workflow, verify no old release job is running, temporarily enable UI, and create the unused immutable namespaced tag on verified current main. Publish its GitHub release to trigger verification and obtain human environment approval. Do not change showcase, Docs or apex deployment workflows.
6. Accept both registry archives, clean consumer and signed provenance before retiring old trust. Remove the enable variable at the end of the window unless ongoing publishing is separately approved.

Tokens and UI cannot publish atomically. If tokens succeeds and UI fails, stop and disable publishing; do not republish tokens, overwrite or unpublish a version, move/reuse a tag or automatically return to the old publisher. Any partial release or forward version recovery requires maintainer review and fresh authority.

Run `pnpm verify:publisher`, `pnpm verify:boundaries`, full `pnpm verify`, and Actionlint/Zizmor against installed workflows. Rehearse packing/transfer from a clean commit with `ui-artifacts.mjs pack` and `verify`; neither publishes. Hosted Linux/Windows and security checks remain required.
