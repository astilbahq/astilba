# Cache contributor guidance

This workspace contains an unreleased caching library, deterministic test harness, and local Workers fixtures. This migration snapshot is not approved for publication or deployment.

Use Node 24.18.1 and pnpm 11.17.0 from the monorepo root. Run the root frozen install; this product has no independent active lock or workspace file. Keep dependencies and Actions pinned; security updates must include the lockfile and their verification evidence.

## Code and tests

- Preserve every existing invariant/scenario assertion and test name. Changes to these files need explicit maintainer approval; the migration's approval covers comments only.
- Run invariant tests with `pnpm exec vitest run --project invariants` and typechecks with direct `tsc`. Vite+ is the convenience loop, not a substitute for those commands.
- Kernel code receives clocks and randomness as inputs. Runtime clocks, timers, and bindings belong in adapters and need a nearby `PLATFORM:` explanation.
- Retain numeric contract references and safety explanations. Do not include private source documents or internal review narratives.
- Keep the fake-based correctness suite separate from conformance, workerd integration, and deployed measurements. Do not weaken checks or add retries to deterministic tests.
- An added branch needs a test that fails when that branch is disabled. Keep the mutation score floor intact.
- The built package must keep one shared kernel module behind all entry points. Run the packaging/import-graph checks after changes to build configuration.
- Existing lint directives and the `@testExport` marker have tool effects. Preserve those effects during comment edits.

## Verification and authority

`SPEC-VERSION` records the immutable reference for this snapshot. Maintainers verify its ancestry and scoped specification-tree equality against their approved private reference checkout. Public CI does not fetch that checkout or require its credentials. Contributors use the checked-in API, deterministic tests, and public guidance; do not invent behavior when they conflict.

Run lint/format, all direct typecheck programs, invariant/unit/conformance/demo tests, packaging checks, both Knip modes, generated-type checks, local boot witnesses, and the unsuppressed security scanner. The workflow inventories each lane. The integration result is still reported separately from the aggregate: review all failures, and block a merge on an unrecognized failure. Do not treat the aggregate alone as integration clearance.

Use isolated checkouts for edits and mutation runs. Do not publish, deploy, change repository visibility, or import private Git ancestry as part of ordinary verification.

Run `pnpm verify:cache` at the monorepo root. Native formatter/linter adapters read the unchanged Cache policy with nested discovery disabled; the compiler and type-aware passes remain enabled. The local workflow census mirrors the active root Cache workflow and is checked by root boundary tests. Preserve that equality and keep `jobs` last in the YAML so the frozen negative-control fixture still appends a job to the job map.
