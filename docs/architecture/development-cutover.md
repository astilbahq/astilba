# Canonical development cutover

`astilbahq/astilba` is the destination for new source PRs, issues and dependency work across Env, tokens/UI, docs, apex and the sanitized Cache snapshot. Env 0.3.1 completed its separately approved monorepo publication; publishing is disabled again and its old npm binding is retired. UI/tokens are private workspace packages; their legacy npm packages and publishers are retired. Production deployment authority remains with the old repositories, which remain unarchived. [Inactive deployment preparation](../deployment-cutover.md) records the remaining activation gates.

## Authority boundaries

| Area | Development | Release or deployment |
| --- | --- | --- |
| Env | `packages/env` | Verified monorepo 0.3.1 release; publisher disabled |
| Tokens and UI | `packages/tokens`, `packages/ui` | Workspace-only; legacy registry packages and publisher retired |
| UI showcase | `apps/ui-showcase` | Existing UI production workflow |
| Docs and apex | `apps/docs`, `apps/site` | Existing Docs production workflows and smoke checks |
| Cache | `products/cache`, approved sanitized snapshot only | Unreleased and non-publishable; no production cutover |

Package names and runtime contracts remain unchanged. Monorepo web apps consume workspace UI/tokens; released Env and independent fixture pins remain unchanged. Env and UI local repository/homepage/issues metadata identifies the monorepo with exact package directories; the Env workflow remains disabled and the UI workflow is removed. Tokens/UI source remains coordinated at 0.6.0; old Docs production retains exact 0.5.0 bytes through its verified local-archive bridge, not npm. Existing `v*` tags and releases remain immutable in their original repositories. Env's verified `env/v0.3.1` is not authority for deployments; see [Publisher cutover](../publisher-cutover.md).

Handbook and private Cache Git ancestry remain excluded. Cache's checked-in authority pin and maintainer-only ancestry/scoped-tree checks remain in force. Unmerged private Cache work is not included merely because it exists in the old repository; it needs its own authority reconciliation, publication review and verified source change.

## Development acceptance

The integrated tree passed the hosted Linux/Windows matrices, UI browser contracts, exact Env archive/consumer gates, Cache integration and source-integrity checks, and security scans. PR #6 consolidated the parser security fix and deterministic alarm validation; the complete 2,525-mutant ratchet passed at 59.49% against the unchanged 55% floor. Main-branch verification then passed, and CodeQL marked the backtracking finding fixed.

[Repository settings](../repository-settings.md) records the live enforcement baseline. Source contributors use the root frozen install plus the independent registry-fixture install, scoped guidance and `pnpm verify`; no verification command publishes or deploys production.

## Outstanding public PR disposition

The [15 October disposition list](../legacy-pr-disposition.md) supersedes the original development-cutover inventory. It covers all sixteen open public legacy PRs and the boundary for six private Cache PRs. It does not merge, close, transfer or mechanically cherry-pick any work. Private implementation details remain in maintainer-local evidence. Cache #94 remains excluded. Reconcile useful candidates against the shared lock and supported contracts before a separately approved replacement.

## Activation and later cutovers

1. Merge this development guidance only after its fresh checks and required reviews complete. Announce the development source boundary; assess any old-repository PR submitted afterward as legacy work rather than a second source of truth.
2. Prepare migration notices for the old repositories and migrate useful issues deliberately. Do not close legacy PRs or disable their automation without approval; old production workflows remain live.
3. Env completed its separately approved publisher handover and is disabled again. UI activation is superseded by [workspace-only retirement](../ui-retirement.md); its external publisher and registry removal are complete. Preserve the legacy Docs local-archive bridge and recovery evidence. Deployment switching remains a separate cutover.
4. Preserve original Docs content dates and source attribution before production cutover; snapshot import dates are not original content dates. Test deployment artifact identity, routes, smoke checks and rollback before switching production authority.
5. Archive old repositories only after those cutovers and outstanding-work decisions are accepted. Preserve historical tags, releases and the private reference needed by Cache's authority gate.

Affected-only execution, remote caching, Cache publication and production measurements are not prerequisites for this development-source cutover and remain separately scoped work.
