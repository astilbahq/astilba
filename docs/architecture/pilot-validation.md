# Astilba pilot validation

The local pilot passes the complete root source verification command and the public package consumer gates available on this Mac. It is ready for migration review, with Linux and Windows CI execution and repository-bound security enforcement still required before a cutover decision.

The branch is `codex/monorepo-pilot` in a clean isolated checkout. Source baselines and the complete CI inventory are recorded in `verification-inventory.md` and its JSON companions. Cache and Handbook are outside the pilot. Existing repositories, npm trust, release tags, domains and production deployments retain their current authority.

## Verification results

| Check | Result on 12 October 2026 |
| --- | --- |
| Root and independent registry frozen installs | Passed with pnpm 11.17.0 and 11.10.0 respectively |
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

The consumer-tested archive has SHA-256 `493975a90d19f23c6a5ba943165ad4d655e87dbae61e92ae5899c2770768d032`. Archive manifests bind the complete clean checkout commit/tree and the nested Env package metadata. Later source commits require a fresh manifest; an existing artifact directory cannot be overwritten. Use a new direct child of `.artifacts` through `ASTILBA_ENV_ARTIFACT_DIR` for another candidate.

All 77 Env `src` files, 48 original UI package files and 96 Docs `src` files retain their baseline bytes. Original registry fixture inputs, generated Env output, manifests and lock are preserved; two scoped lint adapters use the canonical Env policy. Next builds refresh the tracked ambient declaration file, as in the original repository; the baseline declaration is retained in this pilot.

## Migration fixes

Env artifact identity now reads the committed nested package path while canonicalizing directory aliases. It still rejects dirty metadata and a committed metadata symlink that does not match the nested Git blob. Tool containment admits the shared pnpm store and rejects external symlink targets. These behaviors have positive and negative controls.

UI's showcase and Astro fixture paths, Docs/apex shared assets, generated configuration, public skill digests, release-surface checks and package command routing follow their new locations. Web dependencies remain registry releases. Knip has explicit workspace and browser entry points so loading different Playwright versions cannot collide in one process; the browser gates themselves remain intact. Registry lint follows the relocated examples without enrolling them in the source workspace.

## Review and baseline observations

CodeRabbit reviewed the migration changes against the public snapshots at their relocated paths. Its Windows path finding was corrected, and local hook caches are excluded while the tracked design context is retained. Package-manager consumers have separate command lanes. A suggested root install in the Windows Node 22.14 lane was rejected after checking the Node-only import graph and proving the consumer in a checkout without source dependencies. Installing the root there would conflict with the UI development minimum of Node 22.18.

The UI disabled-menu unit assertion intermittently failed in both the pilot and the untouched UI checkout, then passed in the complete root run and the Node 22.18 run. The test and product behavior were retained. Knip's existing `.mdx` configuration hint is informational; no exit codes, assertions or screenshot thresholds were relaxed.

## Remaining acceptance conditions

- Execute the prepared workflows on GitHub-hosted Linux and Windows runners, including the original Ubuntu screenshot baselines and both Windows consumer lanes.
- Run fresh OSV, CodeQL, dependency review and Zizmor checks and configure the destination repository's required statuses and security rules. The inventory captures the existing rules without changing them.
- Choose the destination repository and history strategy. Snapshot imports make the local pilot runnable, but Docs sitemap timestamps currently reflect the import commit; original content dates and source links need an explicit production migration strategy.
- Validate release identities, namespaced future tags, trusted publishing, exact-artifact transfer and each deployment route before switching their authorities. No publisher, production secret, release or deployment is active in the pilot.

Affected-only execution, remote caching, Turbo adoption and source-repository archival are deferred until verification parity and a separate cutover decision.
