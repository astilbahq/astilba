# Astilba monorepo pilot

This isolated pnpm workspace combines public source snapshots. Read `docs/architecture/verification-inventory.md` for the complete gate inventory and `docs/architecture/pilot-validation.md` for validation results.

- `packages/env` owns Env; its scoped guide covers source, artifact and consumer identities.
- `packages/tokens` and `packages/ui` own the interface foundation; read their guides and root `PRODUCT.md` and `DESIGN.md`.
- `apps/ui-showcase` and `fixtures/ui-astro` consume workspace UI source.
- `apps/docs` and `apps/site` own the docs and apex applications; read `apps/docs/AGENTS.md` and `PRODUCT.md`.
- `fixtures/registry/env` is an independent registry consumer workspace. It must retain its own lock, pnpm version and public package pins.

Use Node 24.18.1 and pnpm 11.17.0 for root development. Install root and registry fixtures separately with frozen locks. `pnpm verify:ui`, `pnpm verify:env` and `pnpm verify:web` are scoped source gates; `pnpm verify` runs all three. Root CI adds the complete Env/Windows/runtime matrix and Linux browser contracts. Follow the inventory for archive and exact consumer pins.

Cache and Handbook are outside this checkout. Do not import private source or history. The root, apps, fixtures and tooling stay private; only Env, tokens and UI are publishable packages. Web apps retain registry dependencies; relocation does not authorize adopting workspace HEAD. Release and deployment authority remains with the original repositories. Keep imported workflows inert and root CI free of publishing and production credentials.

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
