# Workspace-only UI and tokens

`@astilba/tokens` and `@astilba/ui` are private workspace packages. Docs, apex, the showcase and the Astro fixture consume their local exports; no new npm UI release is planned. Env remains independently publishable with its enable switch absent. Cache remains unreleased, private and excluded from this retirement.

The migration keeps the existing 0.6.0 source versions, exports, peer ranges, components and CSS. It removes UI publishing configuration, the monorepo `release-ui.yml`, its prepared trust entries and the obsolete legacy version-check command. Publisher identity and release packing explicitly reject retired UI. The remaining UI artifact and attestation readers are offline verification tools, not publishing authority. Package-output, consumer, browser, accessibility and unchanged screenshot gates still run.

Tokens builds during its workspace `prepare` lifecycle before Docs generates Panda bindings. `pnpm verify:web` also builds tokens and UI in dependency order, so the hosted web lane works from a clean checkout without a preceding UI job. Root frozen installs use pnpm 11.17.0; Env application and independent fixture registry pins remain unchanged.

## Preserve the legacy deployment path

This PR changes only the monorepo. The old `astilbahq/docs` root and `site/` continue to install UI/tokens 0.5.0. Its existing verification and production jobs perform frozen dependency installs; deleting those registry versions would break future builds and deployments even when already-built assets exist. Leave all eight published UI/tokens versions, old trust connections, old repository workflows, historical tags/releases and production environments untouched for now. Do not merge a legacy main change that would automatically deploy.

The archived Docs lock and imported deployment workflow remain byte-preserved, inert references. Boundary tests retain their registry pins and frozen workflow digests; they do not contact npm, change the old repository or authorize deployment. Old repositories remain unarchived. Merging this PR does not authorize registry removal or external workflow/environment/tag-rule changes.

Before permanent unpublishing, separately approve and verify one of these preservation paths:

1. Keep the existing deployment authority but replace its npm UI/tokens inputs with pinned local copies of the exact **0.5.0** package bytes. Record their registry integrity and independent hashes, license and provenance; verify a clean frozen install, declarations, both builds, browser/axe contracts, artifact identity and rollback. A bridge must not silently adopt monorepo 0.6.0, relax integrity checks or introduce an unauthenticated mutable URL. Preparing, merging or deploying that legacy change requires separate approval because legacy main deploys automatically.
2. Approve a deployment cutover to workspace source only after original content dates, source attribution, Worker routes, distinct artifacts, smoke checks and rollback are verified. Retain a non-registry-dependent recovery path for the old deployed revision. Deployment cutover remains separate from this PR.

## Later permanent registry removal

Permanent unpublishing is the selected eventual outcome, **not performed or authorized by this PR's merge**. Recheck npm policy and all dependents immediately before removal. For packages older than 72 hours, npm currently requires fewer than 300 weekly downloads, one maintainer and no public-registry dependents. Source version 0.x does not waive that policy. Existing downloads or cached dependencies do not prove the absence of consumers.

After the legacy preservation path is accepted, separately retire the old UI publisher and its two trust connections, leaving showcase deployment untouched. Confirm no publication is in flight. Recheck exact package names, all eight versions, owner authority and eligibility; unpublish UI first, verify its absence, then recheck Tokens dependents and eligibility before removing Tokens. Stop if npm refuses either operation; do not automatically substitute deprecation, publish a replacement, reuse a version or bypass restrictions. Unpublication is irreversible and an unpublished package/version can never be reused.

See [npm's unpublish policy](https://docs.npmjs.com/policies/unpublish/). No publication, deployment, repository archival, Env retirement or Cache #94 inclusion is part of this work.
