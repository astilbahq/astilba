# Management application

The application stores accounts, workspaces and revisioned environment configuration. It provides server enrollment, scoped credentials, reviewed visual/Compose deployment, Git source reviews and exact-commit Dockerfile builds through the Go host agent. See the [agent guide](../../docs/agent.md) for installation and signed updates. It has no dependency on an Astilba account or hosted service.

## Development

Use Go 1.27.4, Node 24.18.1, pnpm 11.28.11 and PostgreSQL 18.4. From the repository root:

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
| `ASTILBA_INSTALLATION_ID` | The installation's ID, from `astilba custody status`. Required when hosted. See [key custody](../../docs/hosting.md#key-custody). |
| `ASTILBA_KEY_BACKEND` | Optional; `file`, the keyring file, is the default and the only key service so far. |
| `ASTILBA_ORIGIN` | Exact browser origin, without a path or trailing slash. HTTPS outside explicit local development. |
| `ASTILBA_LISTEN` | HTTP listener; defaults to `127.0.0.1:8080`. Use a TLS-terminating reverse proxy in production. |
| `ASTILBA_DATABASE_POOL_SIZE` | Connections each process may hold, 4 to 1000; defaults to 12. See [database connections](../../docs/hosting.md#database-connections). |
| `ASTILBA_SECRET_ACCESS_RETENTION_DAYS` | Days to keep secret access records, 30 to 3650; defaults to 400, and hosted keeps 400. Read by `astilba migrate`, so set it where migrate runs. See [operational bounds and retention](../../docs/hosting.md#operational-bounds-and-retention). |
| `ASTILBA_TROUBLESHOOTING` | Optional. `single-instance` declares that one instance serves this installation, which [interactive troubleshooting](../../docs/interactive-troubleshooting.md) requires. Off by default. |
| `ASTILBA_TRUSTED_PROXIES` | Optional comma-separated IPv4 or IPv6 CIDRs for reverse proxies allowed to supply `X-Forwarded-For`. Defaults to no trusted proxies. |
| `ASTILBA_AGENT_RELEASES`, `ASTILBA_AGENT_RELEASE_KEY` | Optional read-only signed agent catalog directory and its trusted public PEM key. Both are required to offer downloadable agent updates. |
| **ASTILBA_INTEGRATION_ORIGINS** | Optional comma-separated exact HTTPS origins for self-hosted GitLab/Gitea; explicit origins may reach private networks. See [Git/build trust boundaries](../../docs/git-builds.md). |
| `ASTILBA_HOSTED` | `true` requires MFA before workspace management writes. Self-hosted owners can enable the same policy per workspace. |
| `ASTILBA_DEVELOPMENT` | `true` enables loopback-only HTTP cookies and optionally plaintext local SMTP or explicit console email. |
| `ASTILBA_SMTP_ADDRESS` | SMTP host and port. Outside explicit local console mode, missing SMTP prevents invitations from being sent; reset requests still return a generic response, and [log that no mail went out](../../docs/hosting.md#when-password-reset-mail-fails). |
| `ASTILBA_SMTP_MODE` | `tls` for implicit TLS or `starttls` for required STARTTLS. `plain` is allowed only for loopback development. `console` writes plaintext email to stdout and requires non-hosted loopback HTTP development with no SMTP address or credentials; `pnpm dev` selects it automatically. |
| `ASTILBA_SMTP_FROM` | Sender address. |
| `ASTILBA_SMTP_USERNAME`, `ASTILBA_SMTP_PASSWORD` | Optional SMTP credentials. |

The bundled image runs as UID/GID 65532 without Node. Its external keyring must be readable by that UID while retaining owner-only permissions. `compose.example.yaml` shows the wiring, not a production TLS or database-backup configuration. Supply secrets through your deployment environment; do not commit local environment files. No runtime Docker socket is mounted into this control-plane application.

Sessions have a 12-hour absolute lifetime and 30-minute idle timeout. The session cookie is HTTP-only, SameSite Strict and uses the `__Host-` prefix. Astilba sets one other cookie, for security only: a browser that completes sign-in holds a device cookie, sent only to the sign-in path, HTTP-only and SameSite Strict with the `__Secure-` prefix. It lasts 90 days from the last sign-in, holds a sealed record of the most recent account and has nothing to do with tracking. Logging out leaves it, because it says this browser has completed sign-in, not that it is signed in. Mutations require the configured Origin and a session-bound CSRF token. Accounts are global identities; authorization comes from a current workspace membership. Owners manage membership/settings, owners and operators edit configuration, and viewers read safe metadata. Security-sensitive changes require the session to have confirmed its identity within 15 minutes, a fixed window long enough to prepare, review and apply a deployment plan. Changes that let a session holder keep or widen access beyond the session, or take the workspace or its secrets elsewhere, require it within five minutes: password, two-factor and recovery-code changes, automation credentials, server creation and enrollment, membership and invitations, the workspace's two-factor requirement, transfer and encrypted export, applying deployment plans with new host changes, which can give root on the server, and enabling pull-request previews, which approves their host changes in advance. Reauthentication asks for one fresh credential: an authenticator or recovery code for an account with two-factor authentication, or the password for an account without it, never both. The session already proved every factor at sign-in, and a browser left signed in often fills in the password by itself, while the authenticator stays with its owner. Codes are checked as at sign-in, so a time step is accepted once and a recovery code is consumed and signs out other sessions. Each account has five reauthentication attempts per 15 minutes, and 20 failures per day across all its sessions; signing in again restarts the window without spending either. Only sign-in and reauthentication restart the lifetime and that window; password, MFA and recovery-code changes rotate the token and keep both. MFA enrollment, password reset and recovery codes invalidate older sessions. Recovery codes are shown once. Password reset emails are sent by a background job after the response, and contain a one-use link to `ASTILBA_ORIGIN/reset?token=…`, valid for 30 minutes. Workspace invitation emails similarly link to `ASTILBA_ORIGIN/invitation?token=…` and expire after 24 hours. These origins come from server configuration, never the request host.


### When a member's authority ends

A member's authority in a workspace ends when an owner removes them or changes their role, and their authority everywhere ends when their authentication version changes (a password change or reset, turning two-factor authentication on or off, replacing recovery codes or using one). What they issued or approved ends with it, in the same transaction as a membership change:

- **Automation credentials** act as the owner who created them. Removing that owner or making them an operator or viewer revokes them, recorded as `automation.revoke` by the owner who made the change. Making them an owner again does not restore them. A changed authentication version already refuses them for good.
- **Invitations and server enrollment or transfer codes** act for the workspace on the issuing owner's say-so. Each counts only while its issuer is still an owner of the workspace with the same authentication version. Removing or demoting the issuer deletes their invitations and clears their codes (`invitation.revoke`, `server.enrollment_withdraw`); a changed authentication version makes them refuse at once. A pending server stays and a current owner issues a new code.
- **Standing automation** runs for the workspace on its approver's say-so: a Git source's automatic imports and deployments, a build target's automatic builds and deployments, a pending automatic deployment, a preview policy, a fork commit's preview approval and a preview cleanup request. Each records its approver and their authentication version, and runs only while they still hold the role it needs: an owner, except a build target's automatic builds, which an owner or operator may approve. Running a build target on demand with an automation credential also needs an owner's approval of it. The SQL function `policy_approver_holds` is that check for Git sources, build targets, automatic deployments, fork approvals, invitations and enrollment codes; preview policies and cleanup requests make the same check together with their host's. Removing or demoting the approver withdraws it in the same transaction (`git.source.approval_withdraw`, `build.approval_withdraw`, `build.automatic_deployment_block`, `preview.policy_disable`, `preview.fork_approval_withdraw`, `preview.cleanup_withdraw`). An owner demoted to operator keeps the automatic builds they approved for targets that don't deploy, but automation credentials can no longer run those targets on demand until an owner approves them. A changed authentication version pauses all of it at once, without any write. The Git and builds section says why a source or build target is paused, and **Approve as mine and resume** approves it again as the person who clicks it, keeping its configuration (`git.source.approve`, `build.approve`); it also lists, for owners, the build targets automation credentials can't run. An approval names the configuration that was shown, so one changed since is refused. Approving needs recent authentication; turning automatic deployment off needs neither and approves nothing. A paused preview policy, fork approval or cleanup is approved again from the previews page. Making the approver an owner again restores nothing.
- Nothing is handed to another owner: no grant or policy ever names an approver who did not approve it.
- Upgrading to the release that introduced these rules withdrew outstanding invitations, enrollment codes and fork approvals, which recorded no issuer, and ended every grant whose holder had already lost the role it needs.
- Workspace configuration such as registries, Git connections, backup destinations and alert webhooks belongs to the workspace and stays. Rotate any secret the departing member could read.
- Work admitted before the change, such as a running deployment, finishes under its normal rules. Open terminal sessions close within five seconds ([interactive troubleshooting](../../docs/interactive-troubleshooting.md)).

Login budgets are per 15 minutes and refuse before any password work. A browser with no device token pays 50 attempts per client source, 200 per IPv6 /48, 5 per account and source, and 10 per account. A browser with one pays 5 per token and 20 per account and auth version. A refusal charges nothing, and PostgreSQL holds the counts for all replicas. Password comparison and hashing run in a queue with two slots per replica and one running hash per IPv4 address, IPv6 /48 or proven account, which refuses with a retryable 503 and no charge when it cannot serve a request in time. [Hosting](../../docs/hosting.md#sign-in-under-attack) explains how these work together.

Client-source abuse limits use each exact IPv4 address or IPv6 /64, with the wider sign-in and signup budgets above. The API walks `X-Forwarded-For` from right to left only while the current address matches `ASTILBA_TRUSTED_PROXIES`; the first untrusted hop supplies the source. Without proxy trust, it uses the immediate peer and ignores forwarding headers. Malformed trusted chains also fall back to the peer. Proxy trust checks use exact addresses before source grouping, and IPv4-mapped IPv6 addresses share the corresponding IPv4 budget. Other forwarded-IP headers are ignored. Multi-instance rate counters and sessions use PostgreSQL.

Browser session refresh cannot recreate a deleted token. A loaded-token save
rejected after logout or renewal returns 401 without setting a session cookie.
A password or MFA change whose session has gone answers the same way, although the
change itself has been applied. Update every management replica and drain requests handled by older binaries
before relying on this revocation guarantee.

A response replaces a cookie the browser sent only when its request replaced or
deliberately ended that session, because a browser applies cookies in arrival order
and a slower response to an older token must not undo a renewal. The cookie therefore
lasts to the absolute lifetime, and the session row's expiry enforces the idle timeout.
A browser holding a dead token keeps it until sign-in, with a CSRF token derived from
it. Cookies issued before this behavior keep their 30-minute lifetime and are not
extended, so people signed in during an upgrade sign in again once, within 30 minutes
of the last older replica's response.

## Revision and key model

OpenAPI lives in `contracts/platform/openapi.yaml`; regenerate Go and TypeScript bindings with `pnpm generate`. Queries and migrations live in this module's `db` directory. `pnpm generate:check` generates into temporary directories and compares bytes, including from a clean checkout.

A project contains logical environments; each starts with one distinct Docker deployment unit and no server assignment. Configuration ownership is fixed at creation as `dashboard` or `compose`; see [Compose source editing](../../docs/compose-deployments.md). Application sources independently describe an image or a GitHub/GitLab/Gitea HTTPS repository and ref; Git descriptors are stored without fetching them until the build milestone. A version 1 specification supports image services, variables, named volumes, database bindings, readiness and typed routes; see [visual deployment](../../docs/visual-deployments.md). Each variable supplies a value or an existing secret-version ID belonging to that environment. Saved specifications are immutable. Their SHA-256 hashes describe the canonical domain encoding, not PostgreSQL's JSON formatting. Edits submit the base revision; a stale edit receives 409 and the current revision. The dashboard retains the losing draft for comparison. Layout positions are stored separately and never create a specification revision.

Each secret version uses a random data key with AES-256-GCM. A second authenticated encryption wraps that key under the active external wrapping key. Purpose, workspace and version identities are authenticated; swapping ciphertext between identities fails. Ordinary APIs return metadata only. Keep database and keyring backups separately protected: neither is useful for recovery without the other.

### Rotating the keyring

To rotate wrapping keys, generate a second private keyring with `keygen --id next --out /path/to/new-key.json`, then create a private combined keyring containing both key IDs and set `active` to the new ID. Stop application writers, back up the database and old keyring, configure the combined keyring and run `astilba custody rewrap --operator <your name> --reason <why>`. This atomically rewraps all secret versions and MFA secrets without changing data ciphertext or revision references, then rewraps the installation's canaries. The installation's key history records the run's start and finish; `astilba custody history` lists them. Restart every instance with the combined keyring before retiring the old key. Retain the old key for older database backups, and for as long as cross-host recovery checkpoints sealed under it are kept, because rewrap does not cover them. If a value cannot be read, rewrap changes nothing, records the run as failed, and its error names the table, row and workspace. Do not replace the original keyring with a new single-key file before rewrap: the canaries would no longer open, and nothing would start.

## Verification and distribution

Set `PLAT_APP_DOCKER_HOST` to an explicitly selected disposable local Docker Unix socket. `make verify-app` runs race-enabled Go integration tests, generation checks, and Chromium/WebKit journeys. The fixtures create randomly named PostgreSQL containers with unique ownership labels and remove only matching containers. They must never use an existing database. `make verify` also retains all P0 experiment gates.

Browser tests build and run the same embedded assets as the image. They use real API sessions and PostgreSQL, not mocked responses. Focused tests protect envelope identity, artifact signatures and atomic replacement; database journeys cover authorization, bootstrap races, revision conflicts, credential replay and invalidation. No coverage threshold is used. Agent tests exercise actual Docker effects, subprocess exits before acknowledgement, duplicate delivery, rollback, credential boundaries and management loss. `make verify-agent-host` runs the native systemd/enrollment/update journey and the visual/Compose deployment, ingress recovery, standalone export and Git/BuildKit/registry fixtures on the explicitly prepared disposable Ubuntu host; CI requires amd64 and arm64.

Build the image from the repository root:

```sh
docker build --build-arg SOURCE_REVISION="$(git rev-parse HEAD)" -t astilba:local .
SOURCE_REVISION="$(git rev-parse HEAD)" pnpm app:source
```

Distribute the generated `.artifacts/p1/astilba-<commit>.tar.gz` with that image. Build from the matching clean commit. Embedded `/source/NOTICE.txt` identifies the exact source commit and includes runtime dependency notices; `/source/LICENSE.txt` contains the AGPL. A development build is explicitly labelled `development` and is not a source-identified release. For a privately hosted fork, make the corresponding source available to its users under the AGPL; a private GitHub link alone does not provide that access.
