# Astilba public web

This repository contains Astilba's public web presence as two independently deployed
workspaces on one canonical origin:

- `../site/` owns `astilba.com`, including the brand home, truthful product pages, global
  sitemap/robots/LLM discovery, and the exact `/docs` → `/docs/` redirect.
- this Starlight application owns `astilba.com/docs/*`, including authored Markdown,
  Pagefind, MCP, Agent Skills, and documentation-specific discovery.

Read `PRODUCT.md` before changing the public interface. Keep the deployment boundary: the
apex shell must not absorb Starlight, and the docs Worker must not answer unrelated apex
paths.

## Content boundaries

- The private `astilbahq/handbook` is context, never a publish source.
- Never quote or copy private specs, errata, ADR rationale, research, red-team material, or incident narrative.
- Derive technical claims from public exports, public behavior, tagged releases, and approved public copy.
- Mark unreleased or incomplete surfaces plainly. Never turn a roadmap into a promise.
- Do not expose parked product ideas merely to fill navigation.

## Information architecture

- `../site/src/pages/` owns only live public surfaces. Do not add pricing, Console, changelog,
  blog, or speculative product routes before those destinations exist.
- `../site/src/styles/global.css` carries the public shell's role-based expression of the same
  Astilba visual system; it must preserve theme state through the shared `starlight-theme`
  key.
- `../site/worker/` owns the exact `/docs` canonicalization only. `/docs/*` belongs to the docs
  Worker Route.
- Starlight owns the global shell and the underlying responsive-navigation, search, and theme behavior.
- `src/components/DocsSidebar.tsx` owns the Base UI product/version menus, recursive collapsibles, and desktop session persistence.
- `src/components/PageActions.tsx` owns the Base UI per-page Markdown actions. Keep the rest of the documentation shell server-rendered unless an interaction genuinely requires another island.
- `src/components/Header.astro` composes the branding, search, theme, and compact global-action controls over those Starlight behaviors. Keep the documentation sidebar focused on product navigation; it has no footer action rail.
- `src/docs/` owns the typed product catalog, independent version trees, page ordering, and destination icons.
- `src/content/docs/` contains public Markdown content.
- Each product owns its sidebar and overview route.
- Prefer six useful pages to sixty empty ones.
- Guides describe an outcome. Concepts explain one mental model. Reference pages document one public surface precisely.
- Sponsor grids, ads, partner placements, and donation blocks do not belong in docs navigation or reading rails.

## Writing style

- Lead with the outcome.
- Use active voice and second person.
- Keep paragraphs short and headings concrete.
- Pair guarantees with their cost or failure behavior.
- Use sentence case for headings.
- Keep examples executable against the documented release state.

## Visual conventions

- Preserve Starlight's native shell behavior. Panda CSS owns Astilba tokens and styles for repository-owned markup; keep `preflight: false` so Starlight remains the reset and shell authority.
- Keep selectors for Starlight, Pagefind, Markdown, and Expressive Code markup in the focused compatibility modules under `src/styles/starlight/`. Do not move those selectors into a component merely to eliminate CSS.
- Use semantic tokens from `panda.config.ts` in owned components. The Starlight token bridge is the only place that should translate Astilba tokens to `--sl-*` variables.
- Keep every theme-bearing value in Astilba's role-based Panda tokens. The Starlight bridge may translate those roles to its numbered compatibility variables, but must never introduce or expose a generic colour scale.
- Use Base UI primitives for sidebar menus and disclosures so keyboard, focus, and screen-reader behavior remain consistent.
- Keep the interface border-led, compact, and nearly monochrome.
- Use Geist for UI and prose, Inter for headings, and JetBrains Mono for code and small utility labels.
- Keep controls and content surfaces square or lightly rounded. Avoid decorative cards and large shadows.
- Use the official theme-aware Astilba logomark with an inline site title in the header.
- Reserve the warm signal accent for focus, links, and meaningful state details.
- Test light, dark, desktop, mobile navigation, code blocks, and production Pagefind search after visual changes.

## Toolchain

- Package manager: pnpm.
- Framework: Astro with Starlight.
- Styling: Panda CSS for Astilba-owned components, plus modular Starlight compatibility CSS.
- `pnpm dev` starts documentation locally; `pnpm --dir ../site dev` starts the apex shell.
- `pnpm panda:codegen` regenerates the ignored typed styling bindings after Panda configuration changes.
- `pnpm lint` runs Ultracite's Oxlint and Oxfmt checks over the JavaScript, TypeScript, and supported configuration surface. Authored Markdown, Astro, CSS, generated files, and vendored material retain their specialized formatters.
- `pnpm lint:fix` applies the safe automatic lint and formatting fixes; inspect its diff before committing.
- `pnpm check` validates Astro, content, and TypeScript.
- `pnpm test` runs focused Vitest coverage for the documentation model and generated destinations.
- `pnpm knip` checks for unused files, exports, and dependencies.
- `pnpm test:browser` builds the production site and runs Chromium and axe coverage. Install Chromium once with `pnpm test:browser:install`.
- `pnpm --dir ../site test:browser` covers the apex shell in desktop and mobile Chromium with axe.
- `pnpm verify` runs both applications' complete local verification sequences against the already-installed browser.
- `pnpm build` produces the docs assets and Pagefind index; `pnpm --dir ../site build:production`
  produces the apex assets and global discovery files.
- Merging to `main` deploys this app through the root `deploy-web.yml` workflow.
- Keep external dependency versions exact. UI and tokens use `workspace:*`; Env remains the released 0.3.1 registry dependency. Root `pnpm verify:web` builds workspace tokens and UI before the web gates.
