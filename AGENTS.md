# Astilba development monorepo

This pnpm workspace is the destination for new Astilba source development. Read `docs/architecture/development-cutover.md` for development versus release authority, `docs/architecture/verification-inventory.md` for the complete gate inventory, and `docs/architecture/pilot-validation.md` for historical validation results. Open new source PRs and issues in `astilbahq/astilba`; do not merge old-repository PRs or transfer their private history as an incidental migration step.

- `packages/env` owns Env; its scoped guide covers source, artifact and consumer identities.
- `packages/tokens` and `packages/ui` own the interface foundation; read their guides and root `PRODUCT.md` and `DESIGN.md`.
- `apps/ui-showcase` and `fixtures/ui-astro` consume workspace UI source.
- `apps/docs` and `apps/site` own the docs and apex applications; read `apps/docs/AGENTS.md` and `PRODUCT.md`.
- `fixtures/registry/env` is an independent registry consumer workspace. It must retain its own lock, pnpm version and public package pins.
- `products/cache` contains only the approved sanitized, unreleased snapshot. Its five workspaces share the root install; read its scoped guide before editing.

Use Node 24.18.1 and pnpm 11.17.0 for root development. Install root and registry fixtures separately with frozen locks. `pnpm verify:ui`, `pnpm verify:env`, `pnpm verify:web` and `pnpm verify:cache` are scoped source gates; `pnpm verify` runs all four. Root CI adds the complete Env/Windows/runtime matrix, Linux browser contracts, and Cache's separate correctness, integration, boot, and mutation lanes. Follow the inventory for archive and exact consumer pins.

Handbook and private Cache history remain outside this checkout. Import only the approved sanitized Cache snapshot, never private source documents or Git ancestry. The root, Cache, apps, fixtures and tooling stay private; only Env, tokens and UI are publishable packages. Web apps retain registry dependencies; relocation does not authorize adopting workspace HEAD. Release and deployment authority remains with the original repositories. Keep archived workflow copies inert and root CI free of publishing and production credentials.

Cache's source-integrity guard protects the pinned executable-source fingerprints in `products/cache/SOURCE-SNAPSHOT.json`, including all existing test assertions and names. The manifest records the explicitly approved duration-parser security amendment; other source and test changes still require maintainer approval. Run the guard after tooling changes. The root UI/control-file policy and the separate product policies must not be mixed through nested-config discovery; each verification lane checks its own canonical policy. Do not run the dirty-route witness concurrently with Cache typechecks.

## Ground rules

Env's installed `release-env.yml` remains disabled by the absent `ENV_PUBLISHER_ENABLED` variable. Read `docs/publisher-cutover.md` before release work; preparing or merging this workflow does not authorize enabling it, changing npm trust or creating tags/releases. Verification CI remains free of publishing and production credentials. UI remains an inactive template and Cache #94 remains excluded.

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
