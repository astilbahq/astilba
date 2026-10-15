# Protected production recovery preparation

This is an **inactive, unprovisioned recovery path**, not permission to deploy. `tooling/deployment/workflows/recover-web.yml` remains outside `.github/workflows`. Its policy rejects execution: all acceptance gates are false and the offline runtime identity is unset. Sole-maintainer self-approval is explicitly approved; manual approval remains required and administrator bypass stays disabled. No environment, credentials, AWS resources or backup uploads are created by this preparation. Publishing remains disabled, old repositories remain unarchived, and Cache #94 remains excluded.

## Recovery contract

The capsule allowlists the retained production baseline, not arbitrary artifacts, URLs, refs or dispatch-supplied scripts. SHA-256, byte length and manifest fingerprint are checked before extraction; every baseline file, including hidden discovery assets, must match. Extraction rejects traversal, duplicates, links and special files. Worker names, routes and precompiled entry points are validated before each write. Copies absorb Wrangler metadata without modifying the verified baseline.

The capsule supplies frozen smoke scripts and their complete local import closure. Recovery does not install packages, rebuild applications, fetch container images from a registry or depend on the removed UI/Tokens npm packages. A separately authenticated offline Linux/amd64 image must contain Node 24.18.1 and Wrangler 4.114.0. Both its archive and loaded image identity must be pinned before activation; version probes run without network or production credentials.

| Scope | Restoration order | Selection |
| --- | --- | --- |
| `site` | apex | Only apex was possibly attempted |
| `docs-site` | Docs → apex | Docs/apex possibly attempted, but not showcase |
| `all` | showcase → Docs → apex | Showcase possibly attempted, or scope uncertain |

Include **attempted**, failed and timed-out writes, not just completed steps. Recovery stops on the first failed restore or per-app smoke. After all selected restores succeed, a final apex → Docs → showcase smoke sweep must pass, even for `site` scope. A repeated Docs suite waits 66 seconds to preserve the existing MCP limiter and assertions. This is not an atomic transaction or a guarantee of uninterrupted service.

## Protected approval and authority

1. A manual current-`main` dispatch supplies the exact SHA, affected scope and a non-confidential incident identifier. An unprivileged plan job exposes those values and the allowlisted capsule hash before approval. Stale main is rejected again after approval and before each restoration.
2. `production-web` must require the named maintainer, prohibit administrator bypass and allow only the `main` branch, not tags. The explicitly approved sole-maintainer policy permits self-approval while retaining required manual review. `environment.json` and `main-branch.json` are unapplied configuration drafts, not evidence of live protection.
3. The protected job verifies live reviewer, bypass, branch and OIDC settings. This repository currently uses an immutable owner/repository-ID OIDC prefix. `aws-trust.json` binds that exact prefix plus `environment:production-web` and `aud=sts.amazonaws.com`; it does not invent an AWS `workflow_ref` condition. Reconcile trust if GitHub identity settings change; do not fall back to a name-only subject. GitHub environment rules enforce main-only access. See [GitHub AWS OIDC guidance](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-aws).
4. Protected environment variables identify two exact S3 keys and non-null VersionIds. The draft reader grants only `s3:GetObjectVersion` for those exact pairs: no listing, mutable latest-object read, write, delete or retention bypass. AWS credentials are short-lived OIDC credentials, not stored access keys.
5. Only the restore invocation receives scoped Cloudflare credentials. Validation, probes, Git, smokes and cleanup get filtered environments without Cloudflare, GitHub or AWS credentials. Raw provider output is not copied into the public journal.

Recovery and forward deployment share the `production-web` concurrency group, with cancellation disabled. This locks monorepo runs only, not legacy repositories. Separately pause legacy entry points before an approved cutover/recovery window; never weaken their historical stale-main guard.

`WEB_RECOVERY_ENABLED` is independent of `WEB_DEPLOYMENTS_ENABLED`: forward deployment can stay disabled during separately approved recovery. Both remain absent now. A switch alone cannot bypass checked-in phase and acceptance gates.

## Failure evidence and retries

Apply intent is appended and fsynced **before** Wrangler, and sanitized events stream to job logs. Write, smoke, integrated-sweep and cleanup outcomes are journaled. Summary and 90-day artifact retention use `always()`; neither is guaranteed after abrupt runner loss. Missing evidence is uncertainty, not proof that nothing changed.

Apply containers have deterministic names owned by run ID, attempt and app. Failure/timeout cleanup and a final always-run step remove only those owned containers without production credentials. Normal termination signals enter cleanup; hard runner loss can still interrupt it. `cleanup-unresolved` is a stop condition. Stopping a local container does **not** undo an accepted Cloudflare operation or prove final provider state.

After any failed, cancelled, timed-out or interrupted write:

1. Keep forward deployment disabled. Stop and inspect any owned container still running; do not overlap recovery writes.
2. Read back provider state for every `apply-started` app, including indeterminate writes and cleanup failures. Without a journal, conservatively include every selected app; if forward scope is unknown, select `all`. Resolve in-flight provider operations before retrying.
3. Preserve sanitized journal and provider readback in incident evidence. Request a fresh current-main dispatch and manual approval for the conservative scope; do not reuse approval to broaden scope or change capsule identity.

## Recommended off-machine backup

Use a **private S3 bucket in a separate AWS account** from everyday development administration. This avoids making Cloudflare or short-lived GitHub artifacts the only recovery copy. Suggested region: `eu-west-2`, subject to account, cost and residency preferences. Store capsule and offline image as separate versioned objects, with checksums, manifest, licenses and runbook. Hold account-recovery instructions independently so GitHub is not the only way to retrieve the backup.

Recommend versioning, public-access blocking, default SSE-S3 and Object Lock **Governance**, initially for 90 days. This duration is proposed, not configured or indefinite: extend or rotate protection before expiry. Object Lock protects versions, not the current key pointer, so pin exact VersionIds. See [S3 Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html).

Object Lock cannot later be disabled, and versioning cannot be suspended. Governance bypass needs special permission: exclude it from uploader and reader, and protect any approved break-glass access separately. Compliance mode is irreversible until expiry; do not select it without an explicit retention decision. See [Object Lock management](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html).

Use a separate least-privileged uploader, not the workflow reader. Exact-version access follows [S3 action permissions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html). SSE-S3 avoids a separately managed KMS key becoming another recovery dependency; KMS would need an equally tested key-recovery policy. Keep actual bucket names, role ARNs, object versions, credentials and deployment IDs out of committed source.

## Required before activation

- Separately authorized environment/protection provisioning using the approved self-approval policy, then live readback.
- Verified Linux/amd64 offline tool archive and image identity, followed by a rehearsal of this actual hosted-runtime contract. The previous Mac rehearsal does not prove an unbuilt image works.
- Separately approved backup account/bucket/retention provisioning, uploads and exact-version readback; fresh-machine extraction and no-registry dry runs. Until then, the current capsule remains local-only.
- Narrow Cloudflare credentials, protected object variables and tested AWS trust/read role, provisioned only with separate approval. Ordinary preparation CI needs no production credentials.
- Independent review and a separate activation diff installing the template, recording verified pins/acceptance, adapting inactive-state tests and enabling only the approved window. Do not activate forward deployment or publishing as a side effect.

Documentation alone does not satisfy durable-backup, protected-environment or activation acceptance. Every forward cutover gate stays false until its own evidence is accepted.
