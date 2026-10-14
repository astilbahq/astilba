# Astilba tokens

Tokens remain framework-neutral static CSS and a Panda preset. Preserve semantic roles, public exports and CSS-only consumers. Tokens and UI retain matching source versions but are private workspace packages, not npm release candidates. Read `../../docs/ui-retirement.md`; preserve the legacy deployment's registry inputs until separately approved removal.

Run root `pnpm verify:ui`. The Docs `prepare` command builds tokens then UI before generating its bindings on clean installs. Build tokens before UI, the showcase and the Astro fixture. Keep exact dependency and Action pins; preserve package acceptance and accessibility gates.
