# Astilba UI

Read root `PRODUCT.md` and `DESIGN.md` for the interface contract. Preserve accessible React controls, Base UI behavior, static CSS and focused exports. Links that look like buttons remain anchors.

Run root `pnpm verify:ui`. Tests cover observable behavior, packing, consumers, browser accessibility and screenshots. Do not update screenshots to hide a regression. UI and tokens retain matching versions. Local metadata and the installed `release-ui.yml` target the monorepo, but `UI_PUBLISHER_ENABLED` must stay absent until separately approved publication. Existing UI releases and deployment authority remain in the original repository; read `docs/ui-activation.md`.
