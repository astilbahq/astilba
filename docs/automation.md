# Environment automation

Owners can issue expiring credentials for CI, the Go CLI and a stdio MCP client from an environment’s **Automation** section. Each credential grants selected capabilities for exactly one workspace, project and environment. Create and revoke require ordinary recent identity confirmation in Account security. Save the returned token privately; it is shown once.

| Capability | Access |
| --- | --- |
| `environment:read` | Saved configuration, revisions, runtime, releases and environment operation status |
| `configuration:write` | Save drafts using an explicit base revision |
| `deployment:plan` | Prepare and inspect normal deployment plans |
| `deployment:apply` | Inspect and apply an existing reviewed plan with an explicit idempotency key |
| `build:run` | List and re-run owner-configured build targets; read their operation status |

Select only the capabilities the caller needs. Build-only callers can discover their scope and targets without configuration-read permission. A credential cannot manage accounts, memberships, other credentials, secrets, integrations, host permissions, recovery or controller transfers. Normal owner/recent-identity requirements still gate privileged deployment changes. There is no arbitrary request or remote-shell tool.

## Credential lifetime

Expiry is required, from one minute to 90 days; the dashboard defaults to seven days. At most 100 unexpired, unrevoked credentials can exist in a workspace. The owner list returns at most 200 records, with active credentials first. Revocation retains metadata and audit history.

Only a SHA-256 hash of the random 256-bit secret is stored. Every request requires the creator to remain an owner with the same authentication version. Password, MFA and recovery changes that invalidate browser sessions also invalidate existing automation credentials. Delegated MFA requires issuance from an MFA-confirmed session with MFA still configured. A credential issued before workspace MFA enforcement cannot use it to bypass that policy. Hosted email verification and subscription admission remain in force.

Write admission rechecks the grant after acquiring the workspace lock. Revocation or expiry while waiting prevents the mutation. Already accepted durable operations retain their normal completion and cancellation rules; revoke does not cancel work already admitted.

Audits record the human creator and credential ID. Credentials and their installation-local audit IDs are excluded from version-1 workspace transfer archives; imported records retain the existing importing-owner attribution. Management recovery revokes the workspace’s automation credentials. Owners issue new ones after recovery or transfer.

## CLI

Run `pnpm cli:build` with the pinned toolchain. It produces Linux, macOS and Windows amd64/arm64 binaries plus licence notices under `.artifacts/cli/development`. Set `CLI_VERSION` for a versioned build and `SOURCE_REVISION` to the full public source commit for distribution notices. Corresponding source is available through the repository and the existing `pnpm app:source` archive command.

Set `ASTILBA_ORIGIN` to the exact management HTTPS origin. Supply `ASTILBA_TOKEN` through the CI secret store, or, on Unix, pass `--token-file` with a private regular file readable only by its owner. Windows uses `ASTILBA_TOKEN`; token-file ACL validation is not supported. Token values never belong in command-line arguments, repository files or logs. `--development` permits HTTP only for an explicit loopback IP; redirects and insecure TLS are unsupported.

```sh
astilba-cli context
astilba-cli draft get
astilba-cli draft update --base UUID --file specification.json
astilba-cli plan prepare --base UUID --server UUID
astilba-cli operation get --id UUID
astilba-cli plan get --id UUID
astilba-cli plan apply --id UUID --key UUID
astilba-cli build targets
astilba-cli build run --application UUID --base UUID --target-hash HASH --key UUID
```

Replace `UUID` and `HASH` with the returned identities. Saving a draft does not deploy it. Inspect the completed plan before applying. Use a new UUID for each distinct build/apply request; after a transport failure, reuse that same key to recover the accepted operation while the credential and normal workspace admission remain valid. The CLI never retries mutations automatically. Responses have a size limit and requests time out after one minute; durable work can continue after a client disconnects.

Specifications contain secret-version references, not values from the secret store. Ordinary plaintext configuration remains visible to callers granted configuration-read access. Store sensitive application values as secrets before using automation.

## Configured builds

An owner first configures the build target through the dashboard. The automation caller lists its application ID, current base revision and target hash, then submits only that application, expected hash, base revision and idempotency key. The API freezes the existing credentials, runner, build inputs and limits. A changed target requires a fresh listing and explicit request.

The one-shot run cannot select workspace credentials/runners, replace target policy or automatically deploy its result. Completed artifacts remain reusable. Existing stale-result fencing still rejects genuinely superseded inputs. Automatic webhook deduplication is tracked separately, and an automatic-deploy build cannot use a one-shot artifact to skip its outstanding watched-path deployment.

## MCP

Run the same binary with `mcp` for stdio transport. Configure the client to launch `astilba-cli`, with `mcp` as its argument and `ASTILBA_ORIGIN`/`ASTILBA_TOKEN` supplied privately, or use the private token-file option on Unix. Stdout carries MCP messages; diagnostics use stderr.

The adapter uses the official Go MCP SDK and exposes nine typed tools: `context`, `draft_get`, `draft_update`, `plan_prepare`, `plan_get`, `plan_apply`, `build_targets`, `build_run` and `operation_get`. Mutation annotations distinguish writes from reads; the API enforces actual authority on every call. Tool discovery is private and immediately stale. Incoming stdio frames are bounded to 256 KiB. Remote HTTP/OAuth MCP and pull-request preview environments are separate increments.
