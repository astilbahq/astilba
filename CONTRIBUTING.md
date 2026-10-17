# Contributing

All Astilba source lives in this repository.

Open an issue before investing in a substantial change. Keep pull requests focused and use a [Conventional Commit](https://www.conventionalcommits.org/) title.

## Local setup

```sh
corepack pnpm install --frozen-lockfile
corepack pnpm@11.10.0 --dir fixtures/registry/env install --frozen-lockfile
pnpm --dir apps/ui-showcase test:browser:install
pnpm --dir apps/docs test:browser:install
pnpm verify
```

Use Node 24.18.1 and root pnpm 11.17.0. The independent registry fixture retains pnpm 11.10.0 and its own lock. Run the appropriate install after dependency changes and commit the resulting lockfile. Read the scoped `AGENTS.md` before editing a package.

## Pull requests

- Make changes through a pull request; direct updates to `main` are blocked.
- Add or update tests for observable behaviour.
- Keep generated files and lockfiles in sync with their sources.
- Run `pnpm verify` before opening the pull request.
- Use squash merge after the required checks pass.
- Explain any security, compatibility, or migration impact in the description.
