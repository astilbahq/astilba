# Astilba monorepo pilot

A local pilot combining the public Env, tokens/UI, docs and apex source trees. Cache and Handbook stay separate. Existing repositories remain responsible for package releases and production deployments.

## Development

Use Node 24.18.1 and pnpm 11.17.0 for root development.

```sh
corepack pnpm install --frozen-lockfile
corepack pnpm@11.10.0 --dir fixtures/registry/env install --frozen-lockfile
pnpm test:browser:install
pnpm --dir apps/docs test:browser:install
pnpm verify
```

Scoped commands are `pnpm verify:ui`, `pnpm verify:env` and `pnpm verify:web`. Env archive, registry adoption and exact-runtime consumer lanes have separate commands and runtime pins. Root CI retains the original Linux and Windows matrices; local source checks alone do not establish complete parity.

- [Verification inventory](docs/architecture/verification-inventory.md) maps every source workflow and required status to the pilot.
- [Pilot validation](docs/architecture/pilot-validation.md) records results and remaining acceptance conditions.
- [Agent guidance](AGENTS.md) routes work to package-specific instructions.

## Workspace

```text
packages/     env, tokens, ui
apps/         docs, site, ui-showcase
fixtures/     ui-astro; registry/env (independent installation)
tooling/      migration commands and original install/workflow references
docs/         architecture and UI operational guidance
```

Docs and apex continue to consume released UI/tokens 0.5.0 and Env 0.3.0. UI source remains version 0.6.0. Registry fixtures retain their public pins and independent lock. Tokens and UI keep coordinated versions; Env remains independently versioned.

Snapshot imports are a pilot technique. Destination naming, production history migration, npm publisher cutover and deployment switching remain separate decisions. No publisher or production deployment is active in this checkout.
