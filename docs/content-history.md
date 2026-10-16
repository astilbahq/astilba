# Public Docs content history

The snapshot import does not reset public page dates. `apps/docs/src/docs/content-provenance.json` records all 36 original public Markdown pages from `astilbahq/docs` at `69101c58b1494689a3ec48ebd3e059f5d33ce729`, matched byte-for-byte to monorepo import `a257d04d3e32db5025bac2f8aaa1ed0d0470f09a`. Each entry binds a SHA-256 content fingerprint, the original last-content-change commit and its UTC committer date. No private Handbook or Cache history is included.

## Dates after import

Production builds require complete monorepo history. They validate the manifest's exhaustive coverage and fingerprints against the immutable import tree, then follow each currently catalogued page's first-parent Git history with an explicit 50% rename threshold:

- An unchanged imported page retains its recorded original date, not the October import date.
- A later content edit advances its date to that monorepo commit. Reverting bytes is another content edit, even when the bytes again match the imported version.
- A pure rename or mode-only change preserves the previous content date. A detected rename accompanied by content changes advances it while preserving original attribution.
- A new or copied page starts with its own monorepo creation date and no invented legacy attribution. Git copy detection does not transfer the source page's identity.
- A deleted page must leave the current catalog and sitemap; its historical manifest entry remains intact. Recreating a deleted path starts a new history when Git records a new addition.
- Uncommitted content cannot produce production dates. Local source attribution remains usable while editing existing or new pages in development.

Renames that Git cannot trace, including sufficiently large rewrites, are new histories; do not guess lineage from matching names or bytes. Any explicit identity amendment requires separate reviewed evidence. The historical manifest is not regenerated for ordinary new, edited, renamed or deleted monorepo pages.

## Source links and attribution

The existing **GitHub** page action and generated Markdown `source` field point to `astilbahq/astilba/blob/main/apps/docs/…`. Original attribution stays immutable: imported pages expose their original public commit/path as HTML `dcterms.source` metadata and Markdown `originalSource` frontmatter. Traced renames retain that original path. New pages omit original attribution. No visible layout or navigation changes accompany this metadata.

Built-artifact verification checks every current sitemap page's date, monorepo source URL, original attribution and HTML/Markdown agreement. Real temporary-Git tests exercise imports, unrelated commits, edits, reverts, pure/edited renames, additions/copies and deletions. Existing browser, discovery, security and screenshot gates remain active.

## Reproduce the public-history audit

Use a separate complete bare clone of the **public** Docs repository, never private Cache/Handbook history:

```sh
git clone --bare https://github.com/astilbahq/docs.git /path/to/public-docs.git
pnpm --dir apps/docs exec node scripts/check-content-provenance.mjs /path/to/public-docs.git
```

The verifier reads only the pinned public revision, checks each source page against both the monorepo import bytes and the original source commit bytes, and compares the regenerated metadata to the checked-in manifest. It does not write files, fetch npm packages, publish or deploy. `--generate` prints candidate JSON for a separately reviewed baseline change; it does not replace the manifest. Ordinary CI/builds validate locally without fetching the old repository.

## Deployment remains inactive

This implementation supplies evidence for the content-history/source-link prerequisite, not maintainer acceptance or deployment authority. `tooling/deployment/cutover.json` stays `prepared-inactive` with **all seven gates false**; the [installed manual forward workflow](deployment-activation.md) remains disabled. Artifact comparison, rollback acceptance, runtime security, protected deployment access, legacy handover and explicit activation approval remain separate. Publishing stays disabled, old repositories stay unarchived and Cache #94 stays excluded.
