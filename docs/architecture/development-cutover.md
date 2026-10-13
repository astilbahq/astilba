# Canonical development cutover

`astilbahq/astilba` is the destination for new source PRs, issues and dependency work across Env, tokens/UI, docs, apex and the sanitized Cache snapshot. This is a development-source cutover only: existing package releases and production deployments continue from their original repositories, and no old repository is archived.

## Authority boundaries

| Area | Development | Release or deployment |
| --- | --- | --- |
| Env | `packages/env` | Existing Env repository and npm publisher |
| Tokens and UI | `packages/tokens`, `packages/ui` | Existing UI repository; coordinated versions |
| UI showcase | `apps/ui-showcase` | Existing UI production workflow |
| Docs and apex | `apps/docs`, `apps/site` | Existing Docs production workflows and smoke checks |
| Cache | `products/cache`, approved sanitized snapshot only | Unreleased and non-publishable; no production cutover |

Public package names, versions, repository metadata, registry fixture pins and npm provenance remain unchanged. The private root package identifies this development repository. Existing `v*` tags and releases remain immutable in their original repositories; future namespaced tags and new trusted-publisher identities require a separate release decision.

Handbook and private Cache Git ancestry remain excluded. Cache's checked-in authority pin and maintainer-only ancestry/scoped-tree checks remain in force. Unmerged private Cache work is not included merely because it exists in the old repository; it needs its own authority reconciliation, publication review and verified source change.

## Development acceptance

The integrated tree passed the hosted Linux/Windows matrices, UI browser contracts, exact Env archive/consumer gates, Cache integration and source-integrity checks, and security scans. PR #6 consolidated the parser security fix and deterministic alarm validation; the complete 2,525-mutant ratchet passed at 59.49% against the unchanged 55% floor. Main-branch verification then passed, and CodeQL marked the backtracking finding fixed.

[Repository settings](../repository-settings.md) records the live enforcement baseline. Source contributors use the root frozen install plus the independent registry-fixture install, scoped guidance and `pnpm verify`; no verification command publishes or deploys production.

## Outstanding public PR disposition

This assessment does not merge, close or mechanically cherry-pick existing PRs. Reconcile each against the current shared lock and supported package contract before opening a replacement here.

| Existing work | Disposition |
| --- | --- |
| Env #32: Vitest 4.1.11 | Already covered by the coordinated Vitest/coverage/Vite+ migration; no separate source transfer needed |
| Env #27 and old Cache package-check tooling bump | Monorepo #5 already proposes the remaining Are the Types Wrong 0.18.5 changes; review that shared change once |
| Env #31: pnpm Action update | Candidate for a focused monorepo CI change; preserve consumer runtime pins and verify the exact Action identity |
| Env #30 and #26: publint and Knip | Deferred shared-toolchain candidates; old-repository green checks do not verify the combined workspace |
| Env #33, #29 and #28: Next, Ultracite and workerd | Hold: existing checks fail; diagnose and reprepare against the monorepo before adoption |
| UI #35 and Docs #98: PostCSS override | One shared override update candidate, not two independent merges; current exact pins remain unchanged |
| UI #21 and Docs #53: lock maintenance | Do not transplant old locks; regenerate only for a scoped approved shared update after maturity gates pass |
| UI #19 and monorepo #4: blanket pinning | Do not merge as written: they narrow supported peer ranges; #4 also removes an explicitly permitted test-dependency range and fails the Env archive gate |
| Docs #99: Astro/Starlight update | Hold: Docs verification fails; reprepare both apps together without changing UI/Env registry pins or screenshot assertions |

Private-repository PR details and the complete dated inventory remain in maintainer-local evidence, not in this public document. No dependency versions, locks, original workflows or original PR states change in this cutover.

## Activation and later cutovers

1. Merge this development guidance only after its fresh checks and required reviews complete. Announce the development source boundary; assess any old-repository PR submitted afterward as legacy work rather than a second source of truth.
2. Prepare migration notices for the old repositories and migrate useful issues deliberately. Do not close legacy PRs or disable their automation without approval; old production workflows remain live.
3. Separately approve publisher migration, exact-artifact/provenance acceptance and future tag naming before changing publishable package metadata or npm trust.
4. Preserve original Docs content dates and source attribution before production cutover; snapshot import dates are not original content dates. Test deployment artifact identity, routes, smoke checks and rollback before switching production authority.
5. Archive old repositories only after those cutovers and outstanding-work decisions are accepted. Preserve historical tags, releases and the private reference needed by Cache's authority gate.

Affected-only execution, remote caching, Cache publication and production measurements are not prerequisites for this development-source cutover and remain separately scoped work.
