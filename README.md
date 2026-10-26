# Astilba

An open-source deployment platform for your own servers, with a visual workflow and complete self-hosting.

Astilba is in private alpha. The core application and its seven technical-validation experiments are implemented, and a live private-alpha acceptance run has exercised deployment and recovery. It is under active development and is not ready for production workloads. There is no public hosted signup yet.

The application supports local accounts and workspace permissions, a visual environment editor, server enrollment, and reviewed visual or file-authoritative Compose deployments. GitHub, GitLab and Gitea imports feed exact-commit Dockerfile builds and a validated Railpack path for Node/npm, Go and shell projects. Durable operations, scheduled backups and restores, a supported PostgreSQL 17-to-18 upgrade, resource history and operational alerts are implemented. See the [implementation plan](docs/platform-implementation-plan.md) for acceptance evidence, limits and remaining work.

The management application and host agent are fully self-hostable, without an Astilba account or hosted service. Operators provide their own servers and integrations. A paid managed control plane is planned; its commercial launch remains separate from publishing this source.

## Start here

- [Application setup and verification](services/platform/README.md)
- [Local development preview and frontend conventions](docs/frontend-development.md)
- [Host agent installation, recovery and updates](docs/agent.md)
- [Visual deployment, observations and export](docs/visual-deployments.md)
- [Compose files, source edits and export](docs/compose-deployments.md)
- [Git imports, builds, registries and artifact promotion](docs/git-builds.md)
- [Scoped automation credentials, CLI and MCP](docs/automation.md)
- [Isolated pull-request previews](docs/pull-request-previews.md)
- [Scheduled backups and restores](docs/backups.md)
- [Supported PostgreSQL major upgrades](docs/supported-upgrades.md)
- [Resource history](docs/operational-visibility.md) and [operational alerts](docs/operational-alerts.md)
- [Self-hosting, management backup and controller transfer](docs/hosting.md)

- [Product brief](docs/platform-product-brief.md)
- [Architecture](docs/platform-architecture.md)
- [Implementation plan](docs/platform-implementation-plan.md)
- [Engineering principles](docs/engineering-principles.md)
- [React Flow editor experiment and results](experiments/canvas-editor/README.md)
- [Durable execution experiment and results](experiments/durable-execution/README.md)
- [HTTPS agent transport experiment](experiments/durable-execution/TRANSPORT.md)
- [Container, ingress and standalone export experiment](experiments/durable-execution/HOST.md)
- [Build artifacts and PostgreSQL restore experiment](experiments/durable-execution/BUILD_DATA.md)
- [Complete Cloudflare management hosting experiment](experiments/cloudflare-hosting/README.md)
- [Git provider and source-preserving Compose experiment](experiments/git-compose/README.md)

## Verify

Use Go 1.27.3, Node 24.18.1, pnpm 11.17.0 and a disposable Docker engine. Pull the digest-pinned images and set `PLAT2_DOCKER_HOST` as described in the [durable experiment instructions](experiments/durable-execution/README.md#reproduce). Set `PLAT_APP_DOCKER_HOST` to the same disposable daemon for application database and browser tests. Also pull the two [Git provider images](experiments/git-compose/README.md#reproduce) and set `PLAT7_DOCKER_HOST` to a disposable local daemon with at least 6 GiB memory, then run:

```sh
pnpm install --frozen-lockfile
pnpm --filter @astilba/canvas-spike exec playwright install chromium webkit
make verify
```

The check includes formatting, module checksums, `go vet`, real PostgreSQL/Docker crash recovery and HTTPS agent transport, plus source-preserving Compose edits, real GitLab/Gitea repository workflows and the React Flow browser suite. Host tests run with the race detector; the transport fixture also cross-compiles a Linux controller helper. CI runs the suites in separate required jobs alongside native Ubuntu amd64/arm64 host checks. The latter require the [disposable host setup](experiments/durable-execution/HOST.md). No production credentials or server are required. On Linux, the Playwright install needs `--with-deps` on a disposable development host.

For the Cloudflare packaging check, also install Docker Buildx, set `DOCKER_HOST` to the same disposable daemon, then run `pnpm verify`. Live hosting probes remain separate and require a disposable Cloudflare deployment and external PostgreSQL. For the editor alone, use `make verify-canvas` or [start the interactive fixture](experiments/canvas-editor/README.md#reproduce).

## Licence

Astilba is licensed under [AGPL-3.0-only](LICENSE). Contributions retain their authors' copyright; see [CONTRIBUTING.md](CONTRIBUTING.md).
