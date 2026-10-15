# Legacy PR dispositions

Assessment dated 15 October 2026 against the monorepo preparation base `654fe9a6d2d69d35961c839d24d3875d0193eb71`. Every listed PR was open when inspected. These are proposed dispositions, not dependency adoption approval or a claim that old checks validate the combined workspace. No PR is merged, closed or transferred here. Recheck current heads, package maturity, supply-chain policy and complete verification before a separately approved update.

## Public repositories

| Legacy PR | Proposed disposition | Monorepo evidence or next step |
| --- | --- | --- |
| [Env #39](https://github.com/astilbahq/env/pull/39): esbuild 0.28.2 | Partly covered; assess remaining graph separately | Root resolves 0.28.2, but some app/Env paths retain 0.28.1. Audit the whole shared graph; do not transplant the old lock. |
| [Env #38](https://github.com/astilbahq/env/pull/38): package checker 0.18.5 | Supersede with shared work | Root is 0.18.5, Env/Cache remain 0.18.4. Monorepo [#5](https://github.com/astilbahq/astilba/pull/5) is the remaining shared candidate, not yet accepted. |
| [Env #33](https://github.com/astilbahq/env/pull/33): Next 16.3.3 | Superseded by newer independent example pin | Registry fixture already uses 16.3.8; retain separate locks and consumer/runtime contracts. |
| [Env #32](https://github.com/astilbahq/env/pull/32): Vitest 4.1.11 | Already covered; candidate for later closure | Coordinated Vitest/coverage/Vite+ migration already landed. |
| [Env #31](https://github.com/astilbahq/env/pull/31): pnpm Action 6.1.0 | Reprepare one focused shared CI update | Current root pin is 6.0.9. Preserve independent consumer pnpm and Node versions and verify the exact Action commit. |
| [Env #30](https://github.com/astilbahq/env/pull/30): publint 0.3.23 | Deferred shared-toolchain candidate | Current root/Env pin is 0.3.21; verify exact archives and all package consumers after any update. |
| [Env #29](https://github.com/astilbahq/env/pull/29): Ultracite 7.10.2 | Reprepare with product-specific policy review | Current 7.9.4 policies deliberately differ across UI, Env, Cache and web. Preserve those policies and avoid incidental formatting churn. |
| [Env #28](https://github.com/astilbahq/env/pull/28): workerd update | Reprepare with the runtime matrix | Env's explicit 1.20260724.1 remains; do not substitute root/Cache Wrangler engines or weaken Workers/portable tests. |
| [Env #27](https://github.com/astilbahq/env/pull/27): package checker 0.18.5 | Same shared disposition as #38 | Review monorepo #5 once; do not merge duplicate legacy changes. |
| [Env #26](https://github.com/astilbahq/env/pull/26): Knip 6.32.1 | Deferred shared-toolchain candidate | Current pin is 6.29.0; retain all workspace entries, product-specific exclusions and test-export behavior. |
| [UI #35](https://github.com/astilbahq/ui/pull/35): PostCSS override | Combine with Docs #98 | Root override remains 8.5.28. Reprepare one shared 8.5.29 security update with both web/UI verification; no exception or suppression here. |
| [UI #21](https://github.com/astilbahq/ui/pull/21): lock maintenance | Do not transplant | Regenerate the root lock only for an approved scoped update; preserve independent registry fixture locks. |
| [UI #19](https://github.com/astilbahq/ui/pull/19): blanket pinning | Decline as written; candidate for later closure | Narrowing supported peer ranges is not lock maintenance. Monorepo #4 has the same contract concern. |
| [Docs #99](https://github.com/astilbahq/docs/pull/99): Astro/Starlight cohort | Reprepare both apps together, separate from cutover | Candidate includes Astro 7.3.8, Starlight 0.42.6 and related integrations; current pins are 7.2.8/0.41.4. Preserve Env 0.3.0, workspace UI and assertions; diagnose in the combined workspace. |
| [Docs #98](https://github.com/astilbahq/docs/pull/98): PostCSS override | Same shared disposition as UI #35 | No second legacy merge or old lock transfer. |
| [Docs #53](https://github.com/astilbahq/docs/pull/53): lock maintenance | Do not transplant | Preserve the separately verified legacy local-archive bridge and its recovery path. Any legacy main merge may deploy automatically. |

Useful dependency work can move without changing any legacy PR immediately. Later close redundant PRs only with maintainer approval and a link to the accepted replacement or explicit decline. Retain legacy production-critical patches until deployment authority moves; do not assume future old-repository builds can use the removed npm packages.

## Private repositories and retained work

The complete 22-PR maintainer inventory also contains six private Cache PRs. Their titles, branches, review text and implementation details remain in private evidence, not this public document. Five are dependency candidates to classify against the shared lock without copying private history. Cache #94 remains expressly excluded: do not merge, close, transfer, cherry-pick or expose its work as an incidental retirement step. Its authority reconciliation and any later source admission require separate approval.

Handbook has no open PRs or executable workflows in the inspected inventory. Preserve private access to Cache's immutable authority reference when planning a read-only archive; archival is not deletion or permission to publish its contents. Do not change that reference in this PR.

Monorepo PRs #4, #5, #9 and #12 remain on their own review paths. Their existence does not authorize source changes here or block retaining the legacy backlog until explicit disposition decisions are accepted.
