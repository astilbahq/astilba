# Astilba verification inventory

The pilot combines the public Env, UI, and web sources with the approved sanitized, unreleased Cache snapshot, retaining their verification contracts. Handbook and private Cache ancestry remain outside this checkout. This inventory records original gates. Current policy: Env publication is monorepo-owned and disabled; UI/tokens are private workspace packages with their publishers and npm registry packages retired. Original production deployment authority is preserved through the verified legacy Docs local-archive bridge; see [UI retirement](../ui-retirement.md) and [inactive deployment preparation](../deployment-cutover.md).

## Source baseline

| Source | Commit on main | Role |
| --- | --- | --- |
| `astilbahq/env` | `97ca4db767b13db3f9e84c104b17f04b9d86370b` | Independently released `@astilba/env` 0.3.0 |
| `astilbahq/ui` | `e295ce4aa79278ae0d865a88435f6539542808fe` | Tokens and React UI, both at source version 0.6.0, showcase and Astro fixture |
| `astilbahq/docs` | `69101c58b1494689a3ec48ebd3e059f5d33ce729` | Separately deployed docs and apex applications |
| Approved sanitized Cache snapshot | `6f09d653a4f12e477ed9652c5432ff2d306a861d` | Unreleased Cache, harness, and local Workers fixtures; files only, no private ancestry |

`verification-inventory.json` contains every package script and workflow job from the original Env, UI and web baselines, including commands, matrices, working directories, Action pins, artifact parameters, permissions, and release jobs. The sibling `*-branch-rules.json` files capture the live main branch rules on 12 October 2026. Workflow presence and required merge status are distinct records.

The original JSON inventory covers Env, UI and web sources and remains historical evidence. Cache's approved source and active workflow mapping are recorded in `cache-integration-inventory.json`; its pristine standalone lock/policy and workflow copies are retained under `tooling/migration` as inactive provenance.

## Cache gates

All five Cache workspaces live under `products/cache` and share the root lock and pnpm 11.17.0. No private reference document or Git ancestry is needed by public CI. Cache remains private and unreleased; its immutable E31 authority pin is retained.

`pnpm verify:cache` runs the 220-file executable-source integrity guard, resolved Vitest-cohort guard, the canonical formatter/linter policy with compiler and type-aware checks, direct typechecks, all 777 tests, unit coverage, packaging/import-graph checks, both Knip modes, both app builds, three local Worker boot witnesses, and the generated-route positive/negative witness. Do not run that final witness concurrently with Cache typechecks.

The root Cache workflow retains thirteen separate lanes: check, invariants, sacred-unwritable-by-tools, conformance, integration, worker-types-drift, Bun, Knip, build-smoke, boot-smoke, module-scope-boot, demo, and demo-boot-witness. Boundary tests assert this independent list and equality with the local workflow census. The frozen census's negative controls remain intact. The original aggregate waits for integration but excludes its result; integration must still be adjudicated separately. Root local verification includes it and fails on any failing test.

The scheduled mutation workflow retains its sacred kill oracle, adapter exclusion, and score floor of 55. Generated integration types use the Cache package's explicit Wrangler 4.109.0 dependency; they must not silently pick up the root's different Wrangler version. Bun remains pinned to 1.3.14.

## Env gates

| Gate | Inputs and command | Runtime | Output or acceptance |
| --- | --- | --- | --- |
| Pack exact archive | Frozen source and registry-fixture installs; `check`, `knip`, `verify`, `pack:artifact`, `verify:artifact` | CI Node 24.18.1, source pnpm 11.10.0 | `.artifacts/local`: one archive and manifest bound to a clean source commit, tree, package metadata, bytes, entries and modes |
| Source verification | Tools and filesystem containment; TypeScript 6.0.3 and 7.0.2; Vitest; Vite+ build; public TSDoc and package checks | Supported development Node | `dist`, deterministic tests, export and package acceptance |
| Clean npm and pnpm consumers | Transferred verified archive; `scripts/verify-consumer.mjs npm` or `pnpm` | Node 24.18.1; npm 11.16.0; pnpm 11.10.0 | Fresh installations, archive contents and modes, runtime imports, CLI inventory semantics |
| Workers admission | Verified archive and baseline 0.1.0 fixture; `verify-workers-consumer.mjs` | Node 24.18.1 plus stock workerd | Admitted generated deployment targets; unsupported surfaces rejected |
| Node and TypeScript matrix | Verified archive; `verify-node-consumer.mjs <node> <ts>` | Node 22.14.0, 22.23.2, 24.18.1, 26.5.1 × TS 6.0.3, 7.0.2 | Eight clean declaration and runtime consumers; matching npm pins |
| Portable consumers | `verify-consumer.mjs bun`, `verify-portable-equivalence.mjs` | Node 24.18.1, Bun 1.3.14 and pinned workerd | Archive installation and equivalent portable observations |
| Generation and Vite | `verify-deterministic-generation.mjs`, `verify-vite-consumer.mjs` | Node 24.18.1, Vite 8.1.5 | Deterministic output and browser/server boundary checks |
| Next consumers | `verify-next-consumer.mjs <next> <mode>` | Node 24.18.1; Next 15.5.22 and 16.2.12 × app-static, app-request, pages-static, pages-request | Eight independently installed framework consumers |
| Windows archive | `verify-consumer.mjs npm` | Windows, Node 22.14.0, npm 10.9.2 | Windows invocation, archive modes, runtime and CLI behavior |
| Registry adoption | Independent frozen install; `verify:all` on Linux and `verify:windows` on Windows | Node 24.18.1, pnpm 11.10.0 | Exact public 0.3.0 registry and GitHub Release bytes, integrity, generated output, Node/Workers/Next/Vite examples |

Main rules require the archive, generation/Vite, npm/pnpm/Bun consumers, Windows archive, all eight Node/TypeScript lanes, all eight Next lanes, OSV and the managed CodeQL check. Workers admission and registry adoption still run even though they are not named required statuses. Env's CodeQL status uses an integration outside its tracked workflow files.

## UI gates

| Gate | Inputs and command | Runtime | Output or acceptance |
| --- | --- | --- | --- |
| Base verification | Frozen lock; `verify:base`: Ultracite, recursive type checks, dependency-security tests, package/app tests, Knip, packing checks | Node 22.18.0 and 24.18.0; pnpm 11.10.0 | Tokens → UI dependency ordering; `dist`; public ESM exports; publint and Are the Types Wrong |
| Astro integration | Workspace packages; named Astro fixture build and output test | Same pinned source toolchain | SSR and integration output contract |
| Browser contract | `test:browser:install:ci`, `test:browser`, `deploy:dry-run` | Ubuntu 24.04, Node 24.18.0, Chromium/Playwright 1.61.1 | Keyboard, axe and immutable platform-specific screenshots; showcase `dist` and Wrangler validation |

Required statuses are both Node lanes, Browser contract, PR title, actionlint, dependency-review, osv-scan and zizmor. A separate CodeQL ruleset rejects high security alerts and scan errors.

## Docs and apex gates

| Gate | Inputs and command | Runtime | Output or acceptance |
| --- | --- | --- | --- |
| Source verification | Frozen lock; generated Env check, Panda generation, lint, Astro/TypeScript, both apps' unit tests and Knip | Node 24.18.0, pnpm 11.17.0 | Generated declarations, content/catalog/security tests and complete application checks |
| Docs build | `deploy:dry-run`: production Astro build, Pagefind, agent skills, MCP corpus, discovery, headers and artifact verification | Same source runtime | Docs `dist`, `/docs/*`, Markdown mirrors, corpus, sitemaps and public artifacts |
| Apex build | Apex `deploy:dry-run` and built-site verifier | Same source runtime | Apex `dist`, root discovery, product routes and exact `/docs` redirect |
| Browser acceptance | Docs production Worker browser tests; apex desktop/mobile browser tests | Chromium/Playwright 1.62.0 | Accessibility, search, themes, navigation and route ownership |
| Released Env contract | `env:release:check`; public npm exact-version check before deployment | Node 24.18.0 | Documentation pins 0.3.0 and does not claim an unavailable release |

Required statuses are docs, actionlint, dependency-review, osv-scan, validate and zizmor, plus the CodeQL ruleset. The scheduled Production smoke checks both existing applications hourly. It remains in the original Docs repository.

## Security and repository checks

UI and Docs run SHA-pinned Actionlint, Zizmor, dependency-review, PR-title validation, OSV and CodeQL jobs. Env runs its pinned OSV workflow and has managed CodeQL enforcement. OSV scans recursive lockfiles, including intentional independent consumers. These checks are external or platform-dependent and are not replaced by a local `verify` result. The pilot carries the shared UI/Docs checks at the root and the full Env archive matrix.

## Release and deployment contracts

| Authority | Trigger and identity | Artifact and final acceptance |
| --- | --- | --- |
| Env original release workflow | Push of `v*`; tag must match package version and current main; protected `npm` environment and OIDC | Complete package matrix, transferred archive verification, publish exact bytes, registry archive hash and attestations, then exact GitHub Release assets |
| UI original release workflow | Published GitHub release `v<version>` on main; versions of tokens/UI match; protected `npm` environment and OIDC | Full verification, pack both once, transfer checksums, publish tokens before UI; registry/provenance/clean-consumer confirmation remains the release procedure |
| UI showcase original deployment | Successful main verification; protected `production` environment | Verified `apps/showcase/dist` artifact and digest, stale-main rejection, deploy to `ui.astilba.com`, production smoke |
| Docs and apex original deployment | Successful main verification; protected `production` environment | Distinct verified artifacts/digests, released Env checks, stale-main rejection, apex deployment/smoke before docs deployment/complete smoke |

The release rows above describe historical contracts, not active publisher authorization. Env 0.3.1 uses the monorepo trusted publisher and its old binding is retired; publishing is disabled. UI/Tokens publishing, old bindings and registry packages are retired. Existing tags, releases, domains, Worker routes and production credentials are unchanged. Imported Env and Docs `.github` folders and `tooling/migration/workflows/ui` are inert references. Ordinary verification CI has no publication, production environment or deployment credentials. The installed manual forward workflow remains disabled with all seven acceptance gates false and its enable variable absent; offline recovery remains parked and uninstalled.

## Pilot command routing

| Pilot command | Coverage |
| --- | --- |
| `corepack pnpm install --frozen-lockfile` | Eight public source workspace projects; explicit membership; no registry-fixture discovery |
| `corepack pnpm@11.10.0 --dir fixtures/registry/env install --frozen-lockfile` | Five independently installed registry-fixture projects |
| `pnpm verify:boundaries` | Workspace/private allowlist, local UI/tokens resolution, preserved released Env and legacy deployment pins, retired UI publisher and disabled Env |
| `pnpm verify:deployment` | Disabled manual/main-only forward workflow, seven unresolved activation gates, unapplied protection inputs, exact artifact transfer, preserved routes and deployment ordering; no remote mutation |
| `pnpm verify:ui` | UI base, browser, Astro build and showcase deployment dry-run |
| `pnpm verify:env` | Env lint/format, Knip, tools, containment, both TypeScript compilers, unit tests, build, TSDoc and package gates |
| `pnpm verify:web` | Both apps' source/build/browser gates and released Env surfaces |
| `pnpm verify:cache` | Cache integrity, correctness, coverage, packaging, app builds, boot witnesses and typed-route controls |
| `pnpm verify` | All four product source gates plus boundaries; does not claim Windows or full runtime-matrix acceptance |
| `pnpm verify:env:archive` | Committed clean pilot source, one exact Env archive and manifest verification |
| `pnpm verify:env:consumers` | npm/pnpm/Bun, Workers, portable equivalence, deterministic generation, Vite and all eight Next consumers; run with the baseline Node/npm/pnpm/Bun pins on PATH |
| `pnpm verify:registry` | Independent published Env adoption examples |
| `pnpm verify:registry:lint` | Original Env formatting and syntax-lint rules for the relocated examples, including generated-byte exemptions; source TypeScript checking stays in the Env gate |
| `.github/workflows/env-package-matrix.yml` | Full original Env archive matrix, including all eight Node/TypeScript lanes and Windows minimum-runtime consumer |
| `.github/workflows/pilot.yml` | Both original UI Node lanes, Ubuntu screenshot contract, Docs/apex verification and Linux/Windows registry examples |
| `.github/workflows/cache.yml` | Original Cache lanes adapted to the root install, with active workflow census and fixed hosted runners |
| `.github/workflows/cache-mutation.yml` | Original Cache mutation ratchet, isolated from product releases and deployments |

The pilot pins development pnpm 11.17.0 and Node 24.18.1 across thirteen workspaces. Env and Cache use Vite+ 0.3.0 and matching Vitest/coverage 4.1.11, with Oxfmt 0.64.0 and Oxlint 1.79.0. Their Ultracite rule policies remain product-specific. Consumer pins remain independent. The root age gate is 4320 minutes, build permissions are the union of existing approved esbuild/sharp/workerd permissions, and security overrides retain the original protections. Original source locks and policies are retained under `tooling/migration/install-baselines`. Registry fixture locks and package pins are unchanged. Monorepo web apps now link workspace UI/tokens; Env remains released 0.3.0. Old Docs production retains exact UI/tokens 0.5.0 through its verified local-archive bridge; frozen migration locks remain historical evidence.

Security CI scans the two executable installation graphs: the root lock and independent registry-fixture lock. Archived baseline locks are provenance, not executable installs. No advisory suppression is added.

Snapshot imports let the pilot run without deciding the destination repository's history strategy. Docs sitemap dates depend on Git history; the pilot's import commit is not a production history migration. Preserving original dates and source links remains a cutover acceptance condition. No affected-only execution or remote caching is enabled.
