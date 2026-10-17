# Astilba

The monorepo for Astilba's products: [Env](packages/env), [Cache](packages/cache), the [UI](packages/ui) and [tokens](packages/tokens) foundation, and the [docs](apps/docs), [apex site](apps/site) and [UI showcase](apps/ui-showcase).

## Development

Use Node 24.18.1 and pnpm 11.17.0.

```sh
corepack pnpm install --frozen-lockfile
corepack pnpm@11.10.0 --dir fixtures/registry/env install --frozen-lockfile
pnpm test:browser:install
pnpm --dir apps/docs test:browser:install
pnpm verify
```

`pnpm verify:ui`, `pnpm verify:env`, `pnpm verify:web` and `pnpm verify:cache` check one area at a time.

## Workspace

```text
packages/   cache, cache-harness, env, tokens, ui
apps/       cache-demo, docs, site, ui-showcase
fixtures/   cache-react-router, ui-astro; registry/env (installs Env from npm, separate lockfile)
```

## Releasing and deploying

- **Web:** merging to `main` deploys the apex, docs and UI showcase to Cloudflare.
- **Packages:** bump the version, merge, then push a tag on `main`: `env/v<version>` or `cache/v<version>`. The release workflow publishes to npm with provenance and creates a GitHub release. Cache releases are staged: approve them on npmjs.com (or with `npm stage approve`) to make them live.

See [AGENTS.md](AGENTS.md) for agent guidance and [CONTRIBUTING.md](CONTRIBUTING.md) for contributors.
