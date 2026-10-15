# Workspace-only UI and tokens

`@astilba/tokens` and `@astilba/ui` are private workspace packages. Docs, apex, showcase and the Astro fixture consume their local exports. No new npm UI release is planned. Env remains independently publishable with its enable switch absent. Cache remains unreleased and excluded.

## Completed registry retirement

After separate maintainer approvals, the old UI release workflow was disabled, both legacy npm trust bindings were removed, and both npm packages were permanently unpublished on 15 October 2026. Public registry metadata and all sixteen version archive URLs returned 404 after removal. Published versions 0.1.0, 0.1.1, 0.1.2, 0.2.1, 0.3.0, 0.4.0, 0.5.0 and 0.6.0 of both packages cannot be reused. No deployment or repository archival accompanied removal. Historical source tags and GitHub releases remain intact.

The monorepo keeps coordinated 0.6.0 workspace source versions, exports, peer ranges, components and CSS. There is no `release-ui.yml`, publisher enable variable or prepared UI trust entry. Publisher identity and release packing reject retired UI; offline archive/attestation readers remain verification tools only. Package output, consumer, browser, accessibility and unchanged screenshot gates remain active.

Docs' `prepare` builds workspace tokens, then UI, then Panda bindings without adding lifecycle hooks to token/UI archives. Package-local Docs/site commands resolve compiled UI exports after a clean root install. Root `pnpm verify:web` builds in dependency order. Released Env 0.3.0 and independent fixture registry pins remain unchanged.

## Preserve the deployed legacy build bridge

Legacy Docs PR [#101](https://github.com/astilbahq/docs/pull/101), merged at `2580aa7b7bc0e6b5c574e41df6391c5c2edd6038`, supplies pinned local copies of the original **0.5.0** UI/tokens archives through dependency overrides. Their registry integrity, independent hashes, licenses and provenance are retained. Clean frozen installs, including lifecycle-disabled installs, succeeded without these npm registry entries. Its normal production verification completed and all 186 Docs plus 36 apex output files matched the preceding accepted production bytes. Retained recovery archives must outlive Actions artifact retention.

Do not replace this bridge with monorepo 0.6.0 as an incidental cleanup or rerun a historical deployment expecting rollback: the stale-main guard deliberately rejects old revisions. Future recovery requires a verified current-head path or separately approved artifact restoration. Other third-party dependencies still require npm. The old UI showcase builds its local packages and remains independently deployed from its original repository.

Frozen locks/workflows under migration baselines and `tooling/migration/ui-retirement.json` preserve the pre-retirement preparation boundary. Their old registry pins and phase are historical evidence, not instructions to fetch removed packages or current availability claims. Do not rewrite those immutable provenance fixtures to hide the transition.

## Deployment and repository retirement remain separate

[Inactive deployment preparation](deployment-cutover.md) records unresolved content-date/source attribution, artifact comparison, protected environment, rollback and handover gates. Its workflow template is not installed and all acceptance gates remain false. This preparation does not deploy, enable publishing, recreate trust, alter tag rules or archive any repository. Cache #94 remains excluded. [Legacy PR dispositions](legacy-pr-disposition.md) propose outstanding-work handling without changing PR states.
