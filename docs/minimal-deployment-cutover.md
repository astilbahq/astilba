# Minimal deployment cutover (inactive)

Move deployment authority for the existing apex, Docs and showcase Workers to `astilbahq/astilba`; keep their routes and infrastructure unchanged. Roll back by selecting each Worker's retained production version, not by rebuilding old source or downloading UI/Tokens from npm. No AWS, offline image, new infrastructure or self-hosted runner is required.

This is preparation only. The [minimal activation PR](deployment-activation.md) installs the manual forward workflow behind the absent enable switch and inactive policy. All seven `cutover.json` gates remain false; offline recovery stays parked and uninstalled, publishing stays disabled, old repositories stay unarchived and Cache #94 stays excluded. No environment, credential, enable variable or provider deployment is created here.

## Before asking to activate

1. Refresh the accepted content-history and three-app artifact/route comparison evidence against the proposed main commit. Preserve the already accepted UI/toolchain differences; source dates, redirects, discovery, headers and existing assertions still apply.
2. Capture a **private** checkpoint directly from Cloudflare for `astilba-site`, `astilba-docs` and `astilba-ui-showcase`: account, Worker, current deployment ID and its exact version ID at 100%, asset identity, routes/custom domains, bindings, compatibility configuration and non-versioned settings. Never commit provider IDs or secret values. Record timestamp, source revisions and checkpoint checksum in the private cutover record.
3. Read-only preflight must confirm each target is a retained, available version, there is no split traffic, and live infrastructure/configuration matches the reviewed candidate. Abort on drift, deleted resources, changed secrets, data/schema migrations or uncertain identity. Config-file hashes alone do not establish live equivalence. Refresh immediately before writes after legacy writers have been paused. Cloudflare keeps rollback access to only the 100 most recently published versions.
4. Review the disabled forward workflow and draft protection/credential inputs in [activation preparation](deployment-activation.md). Separately approve configuring `production-web` (manual approval, sole-maintainer self-approval permitted, no administrator bypass, main only) and scoped Cloudflare credentials; record accepted gates and checkpoint identity only after their requirements are met. Use GitHub-hosted runners only. Keep baseline evidence available to the approver and independently of the forward job's success.
5. Obtain explicit approval for the production window, the lack of a live rollback rehearsal and the exact baseline. Pause and drain only legacy production entry points under that approval, including manual reruns and competing provider writers; keep verification intact. Do not deploy from a legacy README change. Transfer scheduled smoke ownership deliberately. Keep rollback access available while disabling forward deployment after the window.

Existing security checks remain requirements. Parking the recovery-image project does not accept unresolved Node/Wrangler risks: triage both the actual forward deployment invocation and the rollback helper's Node fetch/OpenSSL path before activation, without restarting the offline-image project.

## Approved window, not executed yet

Use the existing `deploy-web.yml` candidate: verified apex → apex smoke → Docs → complete web smoke → showcase → showcase smoke. It deploys the three exact verified artifacts, not a rebuild. Preserve the stale-main guard. No new route, binding, setting or resource change belongs in this cutover.

Record each write's **intent before it starts**, then its outcome and provider readback in the private run record. Include failed, cancelled and timed-out attempts. Existing Actions step names identify order; a missing green step is not evidence that no write occurred. If evidence is lost, conservatively treat all three Workers as possibly attempted and inspect provider state. A failure stops later forward writes. Do not automatically launch rollback or overlap an unfinished write.

## Rollback

Rollback is itself a production deployment and requires explicit approval. Stop/drain all writers, keep forward deployment disabled, inspect provider state and resolve any in-flight/indeterminate operation first. Confirm the private checkpoint still matches this account/Worker and the baseline version remains available. Do not select an implicit “previous” version or substitute an unavailable target.

| Last possibly attempted forward write | Restore order |
| --- | --- |
| Apex | `astilba-site` |
| Docs | `astilba-docs` → `astilba-site` |
| Showcase or unknown | `astilba-ui-showcase` → `astilba-docs` → `astilba-site` |

`tooling/deployment/rollback-version.mjs` performs one exact-version operation: GET the target version, GET and require the inspected current deployment ID with single-version 100% traffic, POST that target at 100%, then GET and verify the returned deployment ID and exact traffic allocation. Its CLI rejects this checked-in inactive policy **before any request**. It has no install, build, registry, image, route/settings write, force override or retry. A future protected invocation supplies the baseline version and the freshly inspected current deployment ID for the same Worker, not the pre-cutover deployment ID.

Do not use the pinned Wrangler 4.114.0 `rollback` command in unattended execution: on changed-secret error 10220 its confirmation defaults to yes in CI and retries with `force=true`. The direct [deployment API](https://developers.cloudflare.com/api/resources/workers/subresources/scripts/subresources/deployments/methods/create/) operation avoids that fallback. Provider rejection, transport failure or wrong readback stops the operation; even an error may mean the write was accepted. Inspect before any fresh approved attempt—never repeat a POST blindly.

After each restored app, require provider readback and its unchanged smoke: apex `pnpm --dir apps/docs smoke:site`; Docs `pnpm --dir apps/docs smoke:production`; showcase `pnpm smoke:production`. Stop on failure. Once the selected scope is restored, run the complete apex → Docs → showcase sweep, including apps not selected for rollback. Allow at least 66 seconds between repeated Docs suites to respect the existing MCP rate limiter; preserve all assertions. Run frozen scripts from the reviewed cutover checkout, with provider credentials removed from the smoke environment. A passing HTTP smoke alone does not prove the version restored.

Cloudflare [versions](https://developers.cloudflare.com/workers/versions-and-deployments/) include code, static assets and versioned configuration. A [rollback](https://developers.cloudflare.com/workers/versions-and-deployments/rollbacks/) does not restore mutable resource data or non-versioned routes/settings, and can be blocked by resource/lifecycle changes. This is a bounded rollback for an unchanged-infrastructure cutover, not disaster recovery or an atomic three-app transaction.

## Verification boundary

`pnpm verify:deployment` exercises the exact request controller with a credential-free provider fixture: all three fixed Worker identities, exact 100% version selection/readback, malformed inputs, split/stale state, mismatched target, changed-secret rejection, an accepted write with a lost response, and failed readback. No test calls Cloudflare. The existing retained-artifact rehearsal additionally exercised apex-only, Docs/apex and all-app partial failures and restored baseline smokes locally without registry access or production writes; preserve that evidence rather than rebuilding the parked offline system.

Neither fixture tests nor the earlier artifact replay prove that Cloudflare will accept today's baseline. Read-only provider retention/configuration preflight and production smokes passed on 15 October 2026 UTC (16 October BST); the private checkpoint must be refreshed after legacy writers are paused for an explicitly approved window. Acceptance of the remaining live-rollback limitation is still outstanding. A live provider rollback rehearsal would require separately approved deployment writes; none is performed or implied here. No acceptance flag is changed by these tests.
