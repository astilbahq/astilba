# Astilba Cache

A portable TypeScript caching library (`@astilba/cache`) with tag invalidation, stale-if-error resilience and privacy scopes, plus its deterministic test harness (`packages/harness`), a Workers demo (`apps/demo`) and a React Router build fixture (`apps/rr-fixture`). It shares the monorepo root install; run `pnpm verify:cache` from the root.

## Ground rules

1. **Invariant and scenario tests are append-only.** You may add tests under `packages/cache/test/invariants/**` and `test/scenarios/**`, but never modify, delete, weaken, skip or `.todo` an existing one. If one looks wrong, stop and raise it.
2. **No ambient time or randomness in the kernel.** `packages/cache/src/**` outside `src/adapters/**` must not call `Date.now(`, `Math.random(`, `setTimeout(`, `setInterval(` or `performance.now(`; components receive a `Clock` and `Rng`. Adapters may use platform APIs, each with a nearby `// PLATFORM:` comment. CI enforces both.
3. **Toolchain honesty.** No `any`, no `--no-verify`, no disabled checks. A non-null assertion needs a `// SAFETY:` comment, and an `oxlint-disable` or `ts-expect-error` needs an inline justification. The only allowed `as unknown as` cast is the `OxlintConfig[]` bridge in `vite.config.ts`.
4. **Direct gates.** Typecheck with `tsc --noEmit` and run invariants with `vitest run --project invariants`; `vp` is the convenience loop only.
5. **Test lanes stay separate.** Invariants and conformance are deterministic and never retry; only the workerd integration lane may tolerate timing. Don't point the invariant suite at real drivers.
6. **New branches bring their own test.** A change that adds or alters a kernel branch includes a test that fails when that branch is disabled. The weekly Stryker run keeps the mutation score above its floor.
7. **One kernel module.** The built package must keep one shared kernel module behind all entry points; run `lint:publish` after build configuration changes.
8. **Self-contained comments.** Explain behaviour in plain language. Don't cite external design documents, section numbers or ticket IDs.
