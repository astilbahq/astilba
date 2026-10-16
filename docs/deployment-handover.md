# Legacy deployment and smoke handover

This is preparation, not an approved production window. Keep deployment and publishing disabled, every cutover gate false, backup/offline recovery parked, old repositories unarchived and Cache #94 excluded.

## Unmerged changes

- `astilbahq/astilba`: install `.github/workflows/production-smoke.yml` behind the absent `WEB_SMOKES_ENABLED` variable. Run the unchanged apex and Docs scripts directly, without installing dependencies, a production environment or provider credentials.
- `astilbahq/docs`: remove only `jobs.deploy` from `verification.yml` and the hourly trigger from `production-smoke.yml`. Retain all verification, artifacts, the local UI archive bridge and manual read-only smoke.
- `astilbahq/ui`: remove only `jobs.deploy` from `verification.yml`. Retain unit/package verification, browser contracts, build artifacts and the already disabled publisher.

The legacy PRs must remain unmerged until their individual handover requirements are satisfied and the maintainer approves them. Do not disable either complete verification workflow.

## Smoke transfer before legacy schedule retirement

The replacement remains default-off even if its PR is merged. It accepts schedule and manual dispatch only on canonical main, uses pinned GitHub-hosted Ubuntu and Node, and makes no deployment or publication call. The existing Node pin is not security acceptance: the [runtime remediation](deployment-activation.md#runtime-security-blocked-not-accepted) must cover this smoke runtime before activation. No Node, dependency, lockfile or existing smoke assertion changes are included.

1. Separately approve and merge the replacement, leaving `WEB_SMOKES_ENABLED` absent.
2. Complete and review the vendor-fixed runtime change. Separately approve enabling only `WEB_SMOKES_ENABLED`; leave `WEB_DEPLOYMENTS_ENABLED`, `WEB_RECOVERY_ENABLED` and `ENV_PUBLISHER_ENABLED` absent.
3. Observe a successful main manual run and a successful scheduled run of both unchanged scripts. Confirm the normal Actions failure notifications reach the maintainer; do not deliberately fail production or add a notification integration as part of preparation.
4. Only then approve merging Docs' legacy retirement. The old smoke stays hourly until that merge and remains available manually afterward. If replacement verification fails or no scheduled run is observed, hold the legacy PR and retain old coverage.

The new schedule is minute 47 UTC, offset from the legacy minute 17 to reduce overlap during transfer. This is not a cross-repository lock. Avoid concurrent manual/public smoke suites and allow at least 66 seconds between repeated Docs suites for the existing MCP limiter. The replacement concurrency group queues rather than cancelling a running suite.

This transfers existing hourly apex/Docs coverage only. Showcase's post-deployment smoke remains unchanged; adding hourly showcase coverage requires a separate decision. Read-only smoke activation is independent of production deployment approval and accepts no cutover gate.

## Retiring writers without disabling verification

Legacy Docs and UI both embed production writes in `jobs.deploy` of `verification.yml`. Removing those jobs prevents writes from the new workflow revision, not from every historical run or independent tool. A legacy README change can also trigger deployment before the retirement merges.

For a separately approved window, inventory old runs, reruns, queues and waiting approvals, local CLIs, dashboard access, Cloudflare Builds and other integrations. Exclude legacy production credential delivery through an explicitly approved mechanism, then drain all possibly started writes and inspect provider state. Do not rely on the monorepo concurrency group across repositories. Do not revoke a credential until shared consumers and independently approved rollback access are understood. Leave old verification and read-only smokes available.

These PRs change no environment, token, enable variable, provider integration, source route or binding. They do not close legacy PRs or archive a repository.

## Cutover and rollback remain separate

Follow the [minimal cutover runbook](minimal-deployment-cutover.md) after exact-SHA, checkpoint and window approval. The forward workflow remains manual, gated and inactive. Maintain a private write-intent/outcome record, treat failed or cancelled attempts as possibly accepted, and stop later writes on failure.

Removing the legacy jobs does not create a protected hosted rollback job. The maintainer's independently available runtime and credential route still require approval. Preserve the reviewed exact-version helper and do not force, rebuild old source or blindly repeat a POST to regain service.

After an approved window, disable further forward execution while preserving approved rollback access. Publishing remains disabled throughout.
