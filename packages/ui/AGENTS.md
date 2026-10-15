# Astilba UI

Read root `PRODUCT.md` and `DESIGN.md` for the interface contract. Preserve accessible React controls, Base UI behavior, static CSS and focused exports. Links that look like buttons remain anchors.

Run root `pnpm verify:ui`. Tests cover observable behavior, packing, consumers, browser accessibility and screenshots. Do not update screenshots to hide a regression. UI and tokens retain matching versions. UI and tokens are private workspace packages. The monorepo UI publisher is retired; do not reintroduce a release workflow, publishing configuration or prepared npm trust. Preserve old registry versions and deployment workflows until the legacy build path has a verified non-npm replacement and separately approved retirement; read `../../docs/ui-retirement.md`.
