# Astilba pilot validation

This document records the local pilot evidence from 12 October 2026. Hosted Linux/Windows execution, refreshed security checks and repository protection have since completed; [development cutover](development-cutover.md) records the current source boundary and the separately deferred release, deployment and history work. The dated table below remains historical evidence, not current gate counts.

The branch is `codex/monorepo-pilot` in an isolated checkout. Source baselines and the CI inventory are recorded in `verification-inventory.md` and its JSON companions. The approved sanitized Cache snapshot is integrated under `products/cache`; Handbook and private Cache ancestry remain excluded. Existing repositories, npm trust, release tags, domains and production deployments retain their current authority.

## Verification results

| Check | Result on 12 October 2026 |
| --- | --- |
| Root and independent registry frozen installs | Passed with pnpm 11.17.0 across thirteen workspaces and 11.10.0 for the independent registry fixture |
| Root `pnpm verify` | Passed, including package/app source checks, both TypeScript compilers, Knip, packing, Astro fixture, browser checks and three deployment dry-runs |
| UI minimum runtime | `verify:base` passed under Node 22.18.0; current development checks passed under Node 24.18.1 |
| Unit and boundary checks | Env 322, tokens 2, UI 43, showcase 7, Docs 120, apex 4; 34 dependency-security tests and 3 pilot boundary tests passed |
| Browser and accessibility contracts | Showcase 19, Docs 26 and apex 12 tests passed; existing Darwin screenshots were unchanged |
| Env archive | One 61-entry archive passed exact source/package, hash, entry and mode verification |
| Clean package managers | npm 11.16.0, pnpm 11.10.0 and Bun 1.3.14 independently installed and accepted the same archive |
| Node and TypeScript | All eight combinations of Node 22.14.0, 22.23.2, 24.18.1, 26.5.1 and TS 6.0.3, 7.0.2 passed with their matching npm pins |
| Minimum consumer without source dependencies | A fresh checkout with no root `node_modules` passed the npm archive consumer under Node 22.14.0/npm 10.9.2 |
| Workers and portable runtimes | Stock Workers admission and Bun/workerd portable equivalence passed |
| Generator and Vite | Deterministic generation and Vite 8.1.5 archive consumers passed |
| Next modes | All eight Next 15.5.22/16.2.12 × app/pages static/request consumers passed |
| Registry adoption | Independent public 0.3.0 archive/integrity/release binding, generated output and all four application examples passed |
| Relocated fixture lint | Original Env formatting and syntax-lint policy passed, with the generated-byte exemptions preserved |
| Active workflow validation | Actionlint and publisher/credential boundary tests passed |
| Integrated Cache | All 723 tests, 300-test coverage lane, packaging/import graph, both Knip modes, app builds, three local boot witnesses and typed-route controls passed |
| Cache minimum runtime and Bun | 553 invariant/unit/conformance tests passed under Node 22.18.0; six Bun 1.3.14 smoke tests passed |
| Cache preservation | All 210 executable fingerprints and existing sacred file bytes preserved; live autofixer canary passed |
| Generated Cache Worker types | Original bytes retained with explicit Cache Wrangler 4.109.0 resolution |
| Active installation security | OSV 2.4.0 reported zero advisories across 1,817 package records in both active locks |

The consumer-tested archive has SHA-256 `493975a90d19f23c6a5ba943165ad4d655e87dbae61e92ae5899c2770768d032`. Archive manifests bind the complete clean checkout commit/tree and the nested Env package metadata. Later source commits require a fresh manifest; an existing artifact directory cannot be overwritten. Use a new direct child of `.artifacts` through `ASTILBA_ENV_ARTIFACT_DIR` for another candidate.

The 48 original UI package files and 96 Docs `src` files retain their baseline bytes. Two of Env's 77 source files received formatting-only changes under Oxfmt 0.64.0; normalized executable source matches the preceding pilot. Original registry fixture inputs, generated Env output, manifests and lock are preserved; scoped lint adapters use canonical product policies. Next builds refresh the tracked ambient declaration file, as in the original repository; the baseline declaration is retained in this pilot.

The pre-Cache archive hash above is historical evidence, not the integrated candidate's identity. Repack from the integrated clean commit before consumer verification; `cache-integration-validation.md` records the fresh candidate's results.

## Cache integration

Env and Cache share Vite+ 0.3.0 and matching Vitest/coverage 4.1.11. Root development remains Node 24.18.1 and pnpm 11.17.0; registry consumer pins, the 4320-minute age gate, and build permissions are unchanged. Cache's original local locks and workflows remain inactive provenance, while the root lock owns all five Cache workspaces.

Native formatter/linter adapters prevent nested discovery from evaluating application build configs during correctness checks. They retain the existing Ultracite policies and explicitly run Cache's compiler and type-aware passes. No Cache test assertion, test name, runtime source, or mutation-score floor changed.

The active Cache workflow and local census are checked for equality, an independent thirteen-lane inventory, and planted-job detection. CodeRabbit's missing-independent-lane-list finding was fixed. Its remaining lockfile finding is contradicted by the included regenerated lock and successful frozen install; the integration commit includes that lock.

The tracked-source secret scan reports only Cache's unchanged static fake API key. Generated local Next encryption/preview material is ignored and absent from the tracked-source export. No scanner suppression was added.

## Migration fixes

Env artifact identity now reads the committed nested package path while canonicalizing directory aliases. It still rejects dirty metadata and a committed metadata symlink that does not match the nested Git blob. Tool containment admits the shared pnpm store and rejects external symlink targets. These behaviors have positive and negative controls.

UI's showcase and Astro fixture paths, Docs/apex shared assets, generated configuration, public skill digests, release-surface checks and package command routing follow their new locations. Web dependencies remain registry releases. Knip has explicit workspace and browser entry points so loading different Playwright versions cannot collide in one process; the browser gates themselves remain intact. Registry lint follows the relocated examples without enrolling them in the source workspace.

## Review and baseline observations

CodeRabbit reviewed the migration changes against the public snapshots at their relocated paths. Its Windows path finding was corrected, and local hook caches are excluded while the tracked design context is retained. Package-manager consumers have separate command lanes. A suggested root install in the Windows Node 22.14 lane was rejected after checking the Node-only import graph and proving the consumer in a checkout without source dependencies. Installing the root there would conflict with the UI development minimum of Node 22.18.

The UI disabled-menu unit assertion intermittently failed in both the pilot and the untouched UI checkout, then passed in the complete root run and the Node 22.18 run. The test and product behavior were retained. Knip's existing `.mdx` configuration hint is informational; no exit codes, assertions or screenshot thresholds were relaxed.

## Remaining acceptance conditions

- Hosted Linux/Windows workflows, Ubuntu screenshot baselines, security checks and destination protection are complete. The destination is `astilbahq/astilba`; the integrated Cache validation retains every existing assertion and the 55% mutation floor.
- Resolve the production history strategy. Snapshot imports make development runnable, but Docs sitemap timestamps currently reflect the import commit; original content dates and source links need an explicit production migration strategy.
- Validate release identities, namespaced future tags, trusted publishing, exact-artifact transfer and each deployment route before switching their authorities. No publisher, production secret, release or deployment is active in the pilot.

Affected-only execution, remote caching, Turbo adoption and source-repository archival are deferred until verification parity and a separate cutover decision.
