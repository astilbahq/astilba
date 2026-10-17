# Astilba monorepo

One pnpm workspace for every Astilba product. Use Node 24.18.1 and pnpm 11.17.0.

- `packages/env`: Env, published to npm as `@astilba/env`.
- `packages/cache`: Cache, published to npm as `@astilba/cache`. It also holds the Cache toolchain (lint, format and test projects) for `packages/cache-harness`, `apps/cache-demo` and `fixtures/cache-react-router`.
- `packages/tokens`, `packages/ui`: the interface foundation (private workspace packages). Read `PRODUCT.md` and `DESIGN.md`.
- `apps/docs`, `apps/site`: astilba.com/docs and the astilba.com apex. Read `apps/docs/AGENTS.md`.
- `apps/ui-showcase`: ui.astilba.com, including its Playwright browser tests and Wrangler deploy config.
- `fixtures/ui-astro`: Astro consumer of the workspace UI.
- `fixtures/registry/env`: Env examples installed from the npm registry. It keeps its own lockfile and pnpm version.

Each package may have its own `AGENTS.md`; read it before editing there.

`pnpm verify` runs everything; `pnpm verify:ui`, `pnpm verify:env`, `pnpm verify:web` and `pnpm verify:cache` run one area. Packages share script names where they apply: `lint` (format and lint check), `typecheck`, `test` (runs once; Env and Cache add `test:watch`) and `build`. At the root, `pnpm lint`, `pnpm typecheck` and `pnpm test` run each package's script of that name; `pnpm lint` also runs `lint:root`, which covers the root, tokens, UI, the showcase and the Astro fixture. Merging to `main` deploys the web apps (`.github/workflows/deploy-web.yml`). Pushing an `env/v*` or `cache/v*` tag publishes that package.

## Ground rules

1. Run `pnpm verify` before proposing a commit.
2. Add or update tests for observable behaviour.
3. Keep dependencies and GitHub Actions exactly pinned. Update the lockfile with dependency changes.
4. Do not weaken type, lint, test, packaging, or security gates to make a change pass.
5. Never commit credentials, local environment files, deployment identifiers, or private URLs.
6. Keep changes focused and preserve unrelated work in a dirty worktree.
7. Prefer deterministic behaviour and explicit inputs over ambient time, randomness, or machine state.
8. Keep tokens framework-neutral. React and Panda belong in `@astilba/ui`, not in the CSS-only consumption path.
9. Links that look like buttons remain anchors. Do not route them through the Base UI Button primitive.
10. Use logical CSS properties and preserve visible focus, reduced-motion, and WCAG 2.2 AA behaviour.
11. Stay on the task you were given. Don't add release, deployment or recovery machinery that nobody asked for.
