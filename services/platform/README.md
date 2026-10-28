# Management application

The application stores accounts, workspaces and revisioned environment configuration. It provides server enrollment, scoped credentials, reviewed visual/Compose deployment, Git source reviews and exact-commit Dockerfile builds through the Go host agent. See the [agent guide](../../docs/agent.md) for installation and signed updates. It has no dependency on an Astilba account or hosted service.

## Development

Use Go 1.27.3, Node 24.18.1, pnpm 11.17.0 and PostgreSQL 18.4. From the repository root:

```sh
pnpm install --frozen-lockfile
pnpm generate:check
make check-app
pnpm app:build
```

`pnpm app:build` builds static assets, gathers runtime dependency licences and embeds the assets in `.artifacts/p1/astilba`. A plain `go build ./cmd/server` in this module produces an API-only development binary. Domain and database tests do not require a frontend build.

Create a private external wrapping-key file with `astilba keygen --out /path/to/keyring.json`. Set `ASTILBA_DATABASE_URL` and `ASTILBA_KEYRING` in the process environment, then run:

```sh
astilba migrate
astilba bootstrap --email owner@example.com --name Owner --workspace Personal
astilba serve
```

Bootstrap reads the password from standard input, or from an owner-only regular file with `--password-file`. Do not put a password in the command line. The password must contain 15–128 characters, at most 512 bytes. Bootstrap is a local, transactional, one-time operation. There is no public bootstrap or registration endpoint. The server checks the application and River schema versions and never migrates automatically. The explicit migration command installs both.

For local development only, set `ASTILBA_DEVELOPMENT=true`, `ASTILBA_LISTEN=127.0.0.1:54181` and `ASTILBA_ORIGIN=http://127.0.0.1:54182`, then use `pnpm --filter @astilba/dashboard dev`. The Vite proxy serves the API at the dashboard origin. Use the built binary with origin `http://127.0.0.1:54181` to test embedded assets. Development mode requires a loopback listener and HTTP origin and cannot be combined with hosted mode.

## Runtime configuration

| Variable | Purpose |
| --- | --- |
| `ASTILBA_DATABASE_URL` | PostgreSQL connection URL. Require TLS and certificate verification for a remote database. |
| `ASTILBA_KEYRING` | External JSON keyring path. Regular file, owner-only permissions; never stored in PostgreSQL. |
| `ASTILBA_ORIGIN` | Exact browser origin, without a path or trailing slash. HTTPS outside explicit local development. |
| `ASTILBA_LISTEN` | HTTP listener; defaults to `127.0.0.1:8080`. Use a TLS-terminating reverse proxy in production. |
| `ASTILBA_AGENT_RELEASES`, `ASTILBA_AGENT_RELEASE_KEY` | Optional read-only signed agent catalog directory and its trusted public PEM key. Both are required to offer downloadable agent updates. |
| **ASTILBA_INTEGRATION_ORIGINS** | Optional comma-separated exact HTTPS origins for self-hosted GitLab/Gitea; explicit origins may reach private networks. See [Git/build trust boundaries](../../docs/git-builds.md). |
| `ASTILBA_HOSTED` | `true` requires MFA before workspace management writes. Self-hosted owners can enable the same policy per workspace. |
| `ASTILBA_DEVELOPMENT` | `true` enables loopback-only HTTP cookies and optionally plaintext local SMTP or explicit console email. |
| `ASTILBA_SMTP_ADDRESS` | SMTP host and port. Outside explicit local console mode, missing SMTP prevents invitations from being sent; reset requests still return a generic response. |
| `ASTILBA_SMTP_MODE` | `tls` for implicit TLS or `starttls` for required STARTTLS. `plain` is allowed only for loopback development. `console` writes plaintext email to stdout and requires non-hosted loopback HTTP development with no SMTP address or credentials; `pnpm dev` selects it automatically. |
| `ASTILBA_SMTP_FROM` | Sender address. |
| `ASTILBA_SMTP_USERNAME`, `ASTILBA_SMTP_PASSWORD` | Optional SMTP credentials. |

The bundled image runs as UID/GID 65532 without Node. Its external keyring must be readable by that UID while retaining owner-only permissions. `compose.example.yaml` shows the wiring, not a production TLS or database-backup configuration. Supply secrets through your deployment environment; do not commit local environment files. No runtime Docker socket is mounted into this control-plane application.

Sessions have a 12-hour absolute lifetime and 30-minute idle timeout. Secure cookies are HTTP-only, SameSite Strict and use the `__Host-` prefix. Mutations require the configured Origin and a session-bound CSRF token. Accounts are global identities; authorization comes from a current workspace membership. Owners manage membership/settings, owners and operators edit configuration, and viewers read safe metadata. Secret and credential changes require authentication within five minutes. MFA enrollment, password reset and recovery codes invalidate older sessions. Recovery codes are shown once. Password reset emails contain a one-use link to `ASTILBA_ORIGIN/reset?token=…`, valid for 30 minutes. Workspace invitation emails similarly link to `ASTILBA_ORIGIN/invitation?token=…` and expire after 24 hours. These origins come from server configuration, never the request host. Previously issued, unexpired workspace invitation codes remain valid as the `token` value in the new invitation URL; their original email content is unchanged.

The API does not trust forwarded client-IP headers. Per-IP abuse limits therefore see the immediate peer when a reverse proxy is used; per-account and global limits still apply. Multi-instance rate counters and sessions use PostgreSQL. Before a larger hosted rollout, add an explicitly authenticated proxy trust policy as part of hosting hardening, not an arbitrary forwarded-header override.

Browser session refresh cannot recreate a deleted token. A loaded-token save
rejected after logout or renewal returns 401 without setting a session cookie.
Update every management replica and drain requests handled by older binaries
before relying on this revocation guarantee.

## Revision and key model

OpenAPI lives in `contracts/platform/openapi.yaml`; regenerate Go and TypeScript bindings with `pnpm generate`. Queries and migrations live in this module's `db` directory. `pnpm generate:check` generates into temporary directories and compares bytes, including from a clean checkout.

A project contains logical environments; each starts with one distinct Docker deployment unit and no server assignment. Configuration ownership is fixed at creation as `dashboard` or `compose`; see [Compose source editing](../../docs/compose-deployments.md). Application sources independently describe an image or a GitHub/GitLab/Gitea HTTPS repository and ref; Git descriptors are stored without fetching them until the build milestone. A version 1 specification supports image services, variables, named volumes, database bindings, readiness and typed routes; see [visual deployment](../../docs/visual-deployments.md). Each variable supplies a value or an existing secret-version ID belonging to that environment. Saved specifications are immutable. Their SHA-256 hashes describe the canonical domain encoding, not PostgreSQL's JSON formatting. Edits submit the base revision; a stale edit receives 409 and the current revision. The dashboard retains the losing draft for comparison. Layout positions are stored separately and never create a specification revision.

Each secret version uses a random data key with AES-256-GCM. A second authenticated encryption wraps that key under the active external wrapping key. Purpose, workspace and version identities are authenticated; swapping ciphertext between identities fails. Ordinary APIs return metadata only. Keep database and keyring backups separately protected: neither is useful for recovery without the other.

To rotate wrapping keys, generate a second private keyring with `keygen --id next --out /path/to/new-key.json`, then create a private combined keyring containing both key IDs and set `active` to the new ID. Stop application writers, back up the database and old keyring, configure the combined keyring and run `astilba rewrap`. This atomically rewraps all secret versions and MFA secrets without changing data ciphertext or revision references. Restart every instance with the combined keyring before retiring the old key. Retain the old key for older database backups. Do not replace the original keyring with a new single-key file before rewrap.

## Verification and distribution

Set `PLAT_APP_DOCKER_HOST` to an explicitly selected disposable local Docker Unix socket. `make verify-app` runs race-enabled Go integration tests, generation checks, Chromium/WebKit journeys and a local Worker proxy. The fixtures create randomly named PostgreSQL containers with unique ownership labels and remove only matching containers. They must never use an existing database. `make verify` also retains all P0 experiment gates.

Browser tests build and run the same embedded assets as the image. They use real API sessions and PostgreSQL, not mocked responses. Focused tests protect envelope identity, artifact signatures and atomic replacement; database journeys cover authorization, bootstrap races, revision conflicts, credential replay and invalidation. No coverage threshold is used. Agent tests exercise actual Docker effects, subprocess exits before acknowledgement, duplicate delivery, rollback, credential boundaries and management loss. `make verify-agent-host` runs the native systemd/enrollment/update journey and the visual/Compose deployment, ingress recovery, standalone export and Git/BuildKit/registry fixtures on the explicitly prepared disposable Ubuntu host; CI requires amd64 and arm64.

Build the image from the repository root:

```sh
docker build --build-arg SOURCE_REVISION="$(git rev-parse HEAD)" -t astilba:local .
SOURCE_REVISION="$(git rev-parse HEAD)" pnpm app:source
```

Distribute the generated `.artifacts/p1/astilba-<commit>.tar.gz` with that image. Build from the matching clean commit. Embedded `/source/NOTICE.txt` identifies the exact source commit and includes runtime dependency notices; `/source/LICENSE.txt` contains the AGPL. A development build is explicitly labelled `development` and is not a source-identified release. For a privately hosted fork, make the corresponding source available to its users under the AGPL; a private GitHub link alone does not provide that access.

The optional `apps/platform/worker.ts` serves the same static build and forwards API requests to one configured `API_ORIGIN`. It never accepts a request-supplied upstream. Configure the Go server's browser origin as the Worker origin, preserve Secure cookies, and use HTTPS for the upstream. Redirects are not followed by the proxy. Cloudflare is optional; this adapter does not replace the separately validated Go container/database hosting architecture. P1 performs local adapter checks, not a live Cloudflare deployment.
