# Releasing

The existing `astilbahq/ui` publisher remains responsible for UI releases until a separately approved handover. The monorepo's `release-ui.yml` is installed but disabled by the absent `UI_PUBLISHER_ENABLED` variable; merging it does not authorize publishing. [UI activation](ui-activation.md) records the remaining controls and npm-owner requirements. Env 0.3.1 completed its independent release; production deployments remain unchanged.

`@astilba/tokens` and `@astilba/ui` currently share a version and release together. A release is built from a tag whose commit is already on `main`; the release workflow verifies the repository, packs both packages once, records their checksums, and publishes those exact artifacts in dependency order.

## Release a version

The steps below describe the legacy UI publisher only. Future monorepo releases use `ui/v<version>`, `release-ui.yml` and protected `npm-ui`, after controls, trust and first publication receive separate approval. Do not run legacy steps to exercise the installed disabled workflow.

1. Update both package versions to the intended semver version.
2. Merge the change through a pull request with the required checks.
3. Publish a GitHub release whose tag is `v<version>` and targets the merged commit on `main`.
4. Confirm the `Release` workflow verifies and publishes both packages.
5. Verify the registry versions, provenance attestations, and a clean consumer install.

If a published release fails, never delete, move, recreate, or reuse its tag or version. Fix the failure through a pull request, advance both packages to the next shared version, and publish a new release from the verified `main` commit.

The npm environment and trusted-publisher configuration authorize only `.github/workflows/release.yml` in `astilbahq/ui`. The publish job runs on a GitHub-hosted runner with an OIDC identity and no long-lived npm credential.

The first public release is the only exception: npm requires a package to exist before trusted publishing can be configured. Bootstrap it with the least-privilege temporary npm credential that can create both packages. Store the credential only as a protected `npm` environment secret; never commit or print it. Once both packages exist, configure trusted publishing, disallow token publishing, delete the environment secret, and revoke the credential immediately.
