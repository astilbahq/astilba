# Astilba

The development monorepo for Env, tokens/UI, docs, apex and the approved sanitized, unreleased Cache snapshot. Submit new source changes and issues to `astilbahq/astilba`. Existing repositories remain responsible for package releases and production deployments, remain unarchived, and retain their history. Handbook remains private and outside this repository.

The [development cutover](docs/architecture/development-cutover.md) defines that split of authority and the remaining release, deployment and history work.

## Development

Use Node 24.18.1 and pnpm 11.17.0 for root development.

```sh
corepack pnpm install --frozen-lockfile
corepack pnpm@11.10.0 --dir fixtures/registry/env install --frozen-lockfile
pnpm test:browser:install
pnpm --dir apps/docs test:browser:install
pnpm verify
```

Scoped commands are `pnpm verify:ui`, `pnpm verify:env`, `pnpm verify:web` and `pnpm verify:cache`. Cache's workspaces live under `products/cache` and share the root install. Env archive, registry adoption and exact-runtime consumer lanes have separate commands and runtime pins. Root CI retains the original Linux and Windows matrices and Cache's separate lanes; local source checks alone do not establish complete parity.

- [Verification inventory](docs/architecture/verification-inventory.md) maps every source workflow and required status to the pilot.
- [Pilot validation](docs/architecture/pilot-validation.md) records results and remaining acceptance conditions.
- [Agent guidance](AGENTS.md) routes work to package-specific instructions.

## Workspace

```text
packages/     env, tokens, ui
apps/         docs, site, ui-showcase
fixtures/     ui-astro; registry/env (independent installation)
products/     sanitized, unreleased Cache and its local evidence fixtures
tooling/      migration commands and original install/workflow references
docs/         architecture and UI operational guidance
```

Docs and apex continue to consume released UI/tokens 0.5.0 and Env 0.3.0. UI source remains version 0.6.0; Env source prepares the unreleased 0.3.1 candidate. Registry fixtures retain their public pins and independent lock. Tokens and UI keep coordinated versions; Env remains independently versioned. See the [first Env release plan](docs/env-first-release.md); preparation is not publication approval.

Snapshot imports exclude private Cache ancestry. Original content-date preservation, npm publisher cutover and deployment switching remain separate decisions. Env's local package metadata identifies this monorepo and its release workflow is installed but disabled; UI metadata and its inactive template retain their preparation state. No package has been republished and release/deployment authority remains with the original repositories. See [Publisher cutover](docs/publisher-cutover.md) for the separate first-release approval gates.
