# Astilba public web

The public site and documentation for Astilba products. A small Astro shell owns
`astilba.com`; the Astro Starlight application owns `astilba.com/docs/*` through a narrow
Cloudflare Worker Route.

## Repository surfaces

| Workspace | Public surface | Responsibility |
|---|---|---|
| `site/` | `/`, `/cache`, global discovery | Brand/product shell, truthful lifecycle copy, global sitemap and agent entry points |
| repository root | `/docs/*` | Starlight documentation, Markdown negotiation, Pagefind, MCP, and product-specific discovery |

Both applications share the same typography, near-monochrome palette, warm signal accent,
theme storage key, and public content boundary. They deploy independently so static docs
assets remain isolated from the brand shell and a visual change cannot broaden the docs
Worker's route.

The structure borrows TanStack's useful multi-library context and Better Auth's concise, task-focused writing, while keeping documentation navigation free of sponsor rails, ads, and duplicated marketing links.

## Visual system

- Starlight retains ownership of the global shell behavior, Pagefind search engine, theme state, and code rendering.
- Panda CSS owns Astilba's role-based semantic tokens and the styles for markup rendered by this repository. The Starlight bridge translates those owned roles without exposing a generic colour scale. Panda's reset is intentionally disabled so it does not compete with Starlight.
- Small compatibility stylesheets under `src/styles/starlight/` target markup rendered by Starlight, Pagefind, and Expressive Code.
- Small Base UI React islands own the accessible product/version menus, animated sidebar disclosures, and per-page Markdown actions.
- The header keeps branding, search, repository, sponsorship, and appearance controls together; the sidebar stays focused on product navigation.
- Geist, Inter, and JetBrains Mono provide a compact technical register.
- Near-black ink, near-white canvas, square controls, and a restrained type hierarchy carry the interface.
- The official Astilba logomark adapts to light and dark themes beside the inline site title.
- A warm signal accent is reserved for focus, links, and small state details rather than navigation backgrounds.
- Page descriptions come from frontmatter through the `PageTitle` override.

## Current scope

- Two independently versioned products: the public-alpha Env compiler and the unreleased Cache preview.
- Sixteen progressively disclosed Cache pages backed by a reviewed private-source snapshot, public platform behavior, and approved public copy.
- One unpublished, non-public Cache chaos evidence app documented in the Workers guide, with its required local workerd witness kept separate from deployed measurements and production support.
- Base UI context switching and animated, session-persisted sidebar sections.
- Starlight table of contents, theme control, code presentation, and Pagefind search.
- Visible release-status language wherever a surface is not yet shipped.
- Markdown content under `src/content/docs/`, with products, independent versions, page order, and destination icons defined in `src/docs/`.

## Development

```bash
pnpm install
pnpm dev                 # documentation
pnpm --dir site dev      # apex shell
```

Installation generates Panda's ignored `styled-system/` bindings. Run `pnpm panda:codegen` after changing `panda.config.ts`; the dev and build commands extract component styles automatically. The local site runs at `http://localhost:4321` by default.

## Documentation catalog

Each product has one typed catalog file under `src/docs/products/`. A product declares its default page, independent versions, stable page keys, sidebar sections, and destination icons. Version `basePath` values own their public URLs, so a future archived release can coexist with the current release without imposing one global docs version.

Keep the default version pointed at the release readers should land on. The selectors preserve a stable page key across products and versions when that page exists, then fall back to the destination product's overview.

Site-wide guides that do not belong to a product or version are allowlisted in `src/docs/site-pages.ts`. That list keeps their HTML pages, Markdown siblings, content negotiation, and MCP resources aligned without placing them in a product selector.

## Checks

```bash
pnpm test:browser:install
pnpm verify
```

Development and type checks do not require a deployed origin. Production builds do: `ASTILBA_DOCS_SITE` must be the public HTTP or HTTPS origin, with no path, so canonical links and generated agent resources cannot silently point at the wrong host.

`pnpm verify` regenerates Panda bindings, runs Ultracite's Oxlint and Oxfmt checks, checks both Astro applications and their Workers, runs focused Vitest coverage, checks both workspaces with Knip, validates both Wrangler bundles without uploading them, and drives both production builds in Chromium with axe. The suites cover the apex shell, mobile menu and overflow, theme continuity, truthful lifecycle copy, Markdown negotiation, WebMCP, Pagefind, docs navigation, page actions, and representative light/dark/overlay accessibility states. Install Chromium once per machine with `pnpm test:browser:install`.

The production build creates the Pagefind search index, validates internal links, and then verifies the deployed artifact set under `dist/docs/`, together with the root `dist/_headers` file consumed by Cloudflare Static Assets. Run `pnpm preview` to inspect that build locally.

Repository automation also runs Actionlint, Zizmor, CodeQL analysis for the application and Actions workflows, dependency review, a complete-lockfile OSV scan, and Conventional Commit PR-title validation. Renovate keeps exact package and immutable workflow pins current on a weekly cadence, groups routine updates, and holds major updates in the Dependency Dashboard for approval. Lockfile maintenance, zero-major packages, Wrangler, and GitHub Actions always require a maintainer merge.

## Deployment

The production site is configured for [Cloudflare Workers Static Assets](https://developers.cloudflare.com/workers/static-assets/) at `astilba.com/docs/`. Astro uses `/docs` as its base and writes every HTML and Markdown representation beneath `dist/docs/`. A small Worker entry point handles content negotiation by serving those existing Markdown assets when a canonical page receives an explicit `Accept: text/markdown`; it does not convert content at request time.

The Worker route covers `astilba.com/docs/*`. Fingerprinted assets, generated corpora, Pagefind, security data, robots files, and sitemaps are served asset-first; page requests, Markdown negotiation, and the exact `/docs/mcp` protocol endpoint run through the Worker. An unknown `.md` URL keeps the HTML 404 response. The retired `docs.astilba.com` hostname is not a Worker target: Cloudflare redirects it path-for-path to the canonical `/docs` mount before origin routing.

Validate the generated assets and Wrangler configuration without uploading anything:

```bash
pnpm deploy:dry-run
pnpm --dir site deploy:dry-run
```

For an intentional fallback outside the normal production workflow, deploy the apex first and
then the docs route with credentials that carry the least possible account scope:

```bash
pnpm --dir site deploy
pnpm --dir site smoke:production
pnpm deploy
pnpm smoke:all
```

`wrangler.jsonc` is the deployment source of truth. Its only production target mounts the docs Worker at `astilba.com/docs/*`; the legacy hostname remains outside that Worker deployment. Wrangler also uploads private source maps so production exceptions resolve to the original TypeScript without publishing those maps as site assets.

The production build also derives a Content Security Policy from the exact inline scripts Astro and Starlight emit. Arbitrary inline JavaScript and inline event handlers remain blocked; same-origin executable assets and the narrow WebAssembly evaluation capability required by Pagefind are allowed. Starlight and Expressive Code still require inline styles, so that exception is deliberately limited to `style-src`. Static-asset-first routes receive the policy from `_headers`; Worker-first HTML reads the same generated policy asset and attaches it directly, as required by Cloudflare's routing model. Both paths also apply a Permissions Policy that denies a curated set of unused browser capabilities while explicitly preserving same-origin clipboard and WebMCP access, plus a strict-origin referrer policy. The artifact verifier, Worker tests, browser suite, and production smoke test all fail if these policies are missing or stop matching the built site.

GitHub Actions automatically deploys after the verification job succeeds on `main`. Verification builds each production surface once, validates both with Wrangler and their browser suites, and uploads separate immutable apex and docs artifacts with SHA-256 digests. The deployment job downloads those exact artifacts by ID, requires successful digest verification, deploys the apex before the `/docs/*` route, and runs Wrangler without rebuilding Astro. Hidden generated paths such as `docs/.well-known/` are deliberately included.

Immediately before Wrangler runs, the deployment job checks that its verified revision is still the current `origin/main`; rerunning an older workflow cannot replace a newer deployment. It smoke-tests the apex before changing the docs route, then tests the complete public web. The bounded checks cover root security and discovery, truthful product lifecycle copy, exact `/docs` canonicalization, docs HTML, negotiated UTF-8 Markdown, a fingerprinted asset's immutable cache policy, and an MCP initialize/search exchange. The same read-only pair runs hourly and on demand through the **Production smoke** workflow, or locally with:

```bash
pnpm smoke:all
```

The `production` environment scopes `CLOUDFLARE_ACCOUNT_ID` and `CLOUDFLARE_API_TOKEN`; pull requests can validate Wrangler but cannot access those credentials or deploy. Actions receive read-only repository access by default and cannot approve pull requests. Main-branch protections require the complete verification matrix for administrators as well as other contributors, while CODEOWNERS routes production-path changes to the responsible maintainer. An independent human-approval gate still requires a second eligible repository reviewer and must not be enabled in a way that deadlocks deployments. Cloudflare recommends a custom API token restricted to Workers editing for the Astilba account.

### Roll back production

Wrangler retains deployable Worker versions independently of the GitHub build artifact. With the production Cloudflare credentials loaded, inspect the recent versions and the intended target before changing traffic:

```bash
pnpm exec wrangler versions list --name astilba-docs
pnpm exec wrangler versions view <version-id> --name astilba-docs
```

Roll back to the explicit version ID, record the reason, and run the same smoke test used by deployment:

```bash
pnpm exec wrangler rollback <version-id> --name astilba-docs --message "Rollback: <reason>"
pnpm smoke:production
```

A rollback immediately creates the active deployment but does not roll back Cloudflare resources or bindings. Stop and investigate rather than forcing the rollback if the target version expects a binding that no longer exists. Cloudflare makes only the 100 most recently published versions eligible for rollback.

## Agent-readable documentation

The homepage and each catalogued documentation page have sibling `.md` representations. Product pages include YAML provenance for their canonical page, product, documentation version, lifecycle, and public source file. HTML pages advertise that representation with `rel="alternate"`, point to the site-wide `llms.txt` index with `rel="describedby"`, and return the authored Markdown at the same canonical URL when a client explicitly requests `text/markdown`.

Production builds also create `/docs/llms-small.txt`, `/docs/llms-full.txt`, product-specific Env and Cache document sets under `/docs/_llms-txt/`, `/docs/robots.txt`, the documentation sitemap, and a digest-verified Agent Skills discovery index under `/docs/.well-known/agent-skills/`. The product skills teach agents to consult the public corpus and preserve each release boundary; they do not embed private product material.

The same public build generates a bounded corpus for the stateless MCP endpoint at `https://astilba.com/docs/mcp`. Every published Markdown page is a fixed resource, while `search_docs` and `read_doc` provide read-only compatibility for clients whose resource support is limited. Every accepted POST consumes one unit from a Cloudflare-native per-source-IP, per-colo rate limit; tool calls and resource reads consume an additional unit because they perform the expensive operations. This is an abuse guard rather than a per-user quota: clients behind shared egress share capacity, and requests without Cloudflare's edge address use one anonymous fallback. MCP failures emit structured, searchable error events, while production logs and traces are independently sampled at 10% and 1%. The Worker accepts no arbitrary URL, account state, or private handbook content, and creates a fresh MCP server and transport for every request.

Machine-readable discovery includes an RFC 9727 API catalog, the current experimental MCP catalog and Server Card, and a separate compatibility card for clients that still probe the earlier well-known path. A concise public MCP guide explains the endpoint and its limits. Build verification checks every document, target URL, media type, cache policy, CORS policy, and RFC 8288 link relation before deployment.

A separate, feature-detected WebMCP tool returns the current page's Markdown in bounded chunks. It prefers the current `document.modelContext.registerTool` API, with legacy `navigator.modelContext.registerTool` and `provideContext` fallbacks for compatible browsers and scanners.

Each product document set follows its typed sidebar order. Env identifies its public-alpha npm release and supported integrations; Cache states that the package is unreleased and not installable. HTTP responses and `robots.txt` allow search indexing, real-time AI input, and model training. The production artifact check keeps those signals, negotiated representations, links, and skill digests aligned.

## Keeping product status current

Treat `src/content/docs/env/release-and-support.md` as Env's public-alpha ledger and `src/content/docs/cache/api-status.md` as Cache's preview ledger. Update the relevant ledger first when behavior changes, then reconcile the overview, catalog metadata, runtime status, concepts, and examples against it. Verify Env claims from the public release, package artifact, tagged source, generated contracts, and tests. Verify Cache claims from the reviewed source snapshot, intended public exports, observable behavior, and tests; do not infer shipped support from an internal source file or engineering milestone tag.

## Content boundary

This is a public documentation repository. Never copy private handbook prose, ADR rationale, research, red-team material, or incident narrative into it. Public claims must be independently supported by the reviewed implementation, public behavior, tagged releases, or explicitly approved launch copy, and must state when the underlying source is not publicly accessible.
