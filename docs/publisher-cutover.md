# Env and UI publisher cutover

Status: **Env 0.3.1 verified, publishing disabled again; UI workflow installed, publishing disabled**. Development is canonical in `astilbahq/astilba`. Env's [0.3.1 release](https://github.com/astilbahq/astilba/releases/tag/env/v0.3.1) completed archive/provenance acceptance and its obsolete npm binding is retired. UI publishing and production deployments remain in the original repositories; all old repositories remain unarchived. [UI activation](ui-activation.md) prepares metadata and the disabled workflow only, not trust or first publication. Cache, including private Cache #94, has no publisher path.

## Installed identities

| Product | npm packages | Tag | Workflow | Protected environment |
| --- | --- | --- | --- | --- |
| Env | `@astilba/env` | `env/vX.Y.Z` | `.github/workflows/release-env.yml` | `npm-env` |
| UI | `@astilba/tokens`, then `@astilba/ui` | `ui/vX.Y.Z` | `.github/workflows/release-ui.yml` | `npm-ui` (not configured by this PR) |

Both workflows require their own enable variable, exact public metadata, a clean checkout and a tag pointing to **current** monorepo `main`. `ENV_PUBLISHER_ENABLED` and `UI_PUBLISHER_ENABLED` remain absent. Env uses only tag pushes; UI uses only published GitHub releases and explicitly rejects prereleases. Neither has manual dispatch or an unconditional downstream bypass. Bare `v*`, cross-product tags, root and Cache packages are rejected.

All three packages' local metadata identifies `astilbahq/astilba` with exact nested directories. Env remains independently versioned at 0.3.1; tokens/UI source stays coordinated at 0.6.0. The 14 October live registry check confirms both UI packages already published 0.6.0 from the old repository. A separate release PR must select a new unused coordinated version; never republish 0.6.0. Docs/apex and independent registry fixtures keep their existing released-package pins.

## Verification and artifacts

Env retains its complete archive/runtime/consumer matrix, source-bound artifact verifier and namespaced GitHub release adapter. UI retains full source, browser/accessibility and package acceptance, packs both packages once, and transfers exact archives with commit/tree, entry hashes/modes and a manifest digest supplied independently by the verification job. Publish rechecks current-main identity after protected environment approval.

Only publish jobs receive OIDC permission. GitHub-hosted runners, SHA-pinned Actions and Node 24.18.1/npm 11.16.0/pnpm 11.17.0 are unchanged. Publishing uses exact archives, public provenance and disabled lifecycle scripts, never workspace-recursive publishing or long-lived npm tokens.

Registry verification compares archive bytes, exact versions and clean consumer acceptance, then npm signatures and both signed attestation types. Expected SHA-512 subjects come from independently verified archives bound to the release commit, not registry metadata. Signed provenance must identify the exact monorepo, workflow, tag, commit and GitHub-hosted builder. Protected environment and authenticated trust readback independently establish the environment identity; npm provenance does not serialize that claim. Env also rereads and downloads its exact GitHub release archive/manifest assets.

Run `pnpm verify:publisher`, `pnpm verify:boundaries` and full `pnpm verify`. Actionlint 1.7.12 and Zizmor 1.26.1 audit both installed workflows through their existing root scans. Rehearse UI packing/transfer from a clean commit with `ui-artifacts.mjs pack .artifacts/ui-rehearsal` and `verify <directory> <trusted-manifest-sha256>`; neither publishes. Retain rejection of altered archives, manifests, checksums, extra files and symlinks. Local rehearsal does not prove live UI OIDC acceptance.

## Separately approved UI release window

1. Merge activation preparation only after current-head checks and both eligible bot reviews clear. Keep publishing disabled. Prepare a separate unused coordinated release version; recheck current main, both version histories, old release activity and npm-owner access. Current source 0.6.0 is already published.
2. Configure and read back manual `npm-ui` approval, an explicitly approved self-review policy, no administrator bypass and tag-only `ui/v*` access. Add separate maintainer-only creation and no-bypass update/deletion rules for `refs/tags/ui/v*`. Existing main protection and production environments remain unchanged.
3. An authenticated npm owner with write access to both packages and account 2FA reads all old connections and each package's publishing policy. Record IDs privately. Inside the approved first-publication window, add only GitHub `astilbahq/astilba`, filename `release-ui.yml`, environment `npm-ui` alongside each old binding. Request direct publish without dist-tag management. npm automatically grants staging permission; the preparation manifest's `allowStagePublish: false` is workflow intent, not registry permission evidence. This workflow never stages.
4. After explicit publication approval and verified trust readback, pause only the old UI release workflow, confirm no old release job is running, temporarily enable UI, and create an unused immutable namespaced tag on verified current main. Publish its GitHub release to trigger full verification, then obtain required human environment approval. Do not touch deployment workflows.
5. Accept both exact registry archives, clean consumer and signed identities. Retire old UI trust only after separate approval; disable the enable variable at the end of the window unless ongoing publication is explicitly authorized.

[npm trusted publishing](https://docs.npmjs.com/trusted-publishers/) permits multiple immutable connections and requires a new connection's first successful publication within two days. Add trust only within an approved release window. [The npm trust CLI](https://docs.npmjs.com/cli/v11/commands/npm-trust/) requires package write access and 2FA; authenticated current settings must prevail over older single-connection CLI descriptions. Existing packages need no bootstrap token.

Env's verified first release and separately authorized old-binding revocation are complete. Its monorepo trust is valid, package policy requires 2FA and disallows bypass-2FA tokens, and both its enable switch and old release workflow remain off. No UI trust, environment, tag rule, release or publication changes in this activation PR.

## Stop and recovery

Before publication, remove the product enable variable and stop release/tag creation. Never move, delete, recreate or reuse an existing release tag. Tokens and UI cannot publish atomically: if either archive is accepted and later work fails, disable publishing and investigate that immutable version. Do not overwrite, unpublish, republish tokens or automatically return the old publisher to service. Partial-release handling or forward-version recovery requires maintainer review and fresh authority. Keep deployments, old-repository archival and Cache #94 outside the release window.
