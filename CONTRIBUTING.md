# Contributing

Submit source contributions for Env, tokens/UI, docs, apex and the sanitized Cache snapshot to `astilbahq/astilba`. The original repositories remain unarchived release/deployment authorities; their open PRs are assessed separately, not automatically transferred or merged.

Open an issue before investing in a substantial change. Keep pull requests focused and use a [Conventional Commit](https://www.conventionalcommits.org/) title.

## Local setup

```sh
corepack pnpm install --frozen-lockfile
corepack pnpm@11.10.0 --dir fixtures/registry/env install --frozen-lockfile
pnpm test:browser:install
pnpm --dir apps/docs test:browser:install
pnpm verify
```

Use Node 24.18.1 and root pnpm 11.17.0. The independent registry fixture retains pnpm 11.10.0 and its own lock. Run the appropriate install after dependency changes and commit the resulting lockfile. Read the scoped `AGENTS.md` before editing a product; Cache's existing test assertions, names and 55% mutation floor remain protected.

## Pull requests

- Make changes through a pull request; direct updates to `main` are blocked.
- Add or update tests for observable behaviour.
- Keep generated files and lockfiles in sync with their sources.
- Run `pnpm verify` before opening the pull request.
- Keep the branch up to date and resolve every review thread before merging.
- Use squash merge after the required checks pass.
- Explain any security, compatibility, or migration impact in the description.

See [development cutover](docs/architecture/development-cutover.md) before changing repository identities, release workflows or deployment routes. A development PR does not authorize publishing, deploying or archiving another repository.
