# Frontend development

The production dashboard lives in `apps/platform`. It uses React, TanStack Router/Query, Panda CSS and the generated OpenAPI client. Experiments remain separate. The rationale for the organisation is in [engineering principles](engineering-principles.md) and [agentic development research](agentic-development-research.md).

## Source boundaries

`src/router.tsx` composes routes, session gates and layouts. Features may use TanStack's public navigation components; they must not import our router instance or route definitions. Route-specific state is passed to a feature as typed inputs and callbacks. Shared session query options live in `src/api/session.ts`, alongside the API client.

Keep a feature's components, styling and private helpers together. A single file is sufficient until responsibilities merit a directory. Promote code to a neutral shared module only when another consumer needs it. Avoid a growing catch-all utilities directory or barrels that obscure dependency direction. Existing top-level feature files can move as they are substantially changed; a bulk rename is not a prerequisite for UI work.

`src/styles.ts` holds shared layout styles; new feature-only styles belong beside the feature. `src/theme.ts` supplies runtime theme state. The build-time theme lives in `theme/` and is consumed by `panda.config.ts`:

- `theme/tokens.ts`: primitive values and semantic roles, including light/dark values.
- `theme/global-styles.ts`: document defaults, native controls, focus and accessibility rules.
- `src/assets/`: source brand assets, including the original Astilba SVG lockup.

Generated `styled-system/` output is ignored by Git. `src/api/generated/` is generated from `contracts/platform`; do not hand-edit or format it.

## Local commands

Use the Node and pnpm versions pinned in the repository. From the repository root:

```sh
pnpm install --frozen-lockfile
pnpm --filter @astilba/dashboard dev
```

The package's `prepare` hook generates Panda's types during installation. If lifecycle scripts were disabled, run `pnpm --filter @astilba/dashboard prepare` explicitly. Vite serves port 54182 and proxies API requests to the Go service on 54181; the frontend command alone does not start the backend. See the root setup instructions for the full application and fixtures.

```sh
pnpm --filter @astilba/dashboard check
pnpm --filter @astilba/dashboard lint
pnpm --filter @astilba/dashboard format
pnpm --filter @astilba/dashboard format:check
pnpm --filter @astilba/dashboard check:fix
pnpm --filter @astilba/dashboard build
```

`check` regenerates Panda types, then runs Vite+'s format, lint and type checks. `format` writes formatting and organises imports; `check:fix` also applies safe lint fixes. The root `vite.config.ts` owns shared Oxlint/Oxfmt settings. Each app's Vite config owns its plugins, server and build settings. Generated code, build artifacts and source brand assets are excluded from formatting. Runtime import cycles are errors; erased `import type` references are allowed.

Vite+ replaces Biome and the separate frontend Vite/type-check commands. It is installed locally and pinned, so no global installation is required. `vite` is aliased to the matching `@voidzero-dev/vite-plus-core`; the Vitest override matches Vite+'s bundled runner. The narrow pnpm peer exception for that core alias accounts for its different version number. Keep all three pins aligned when upgrading, while preserving the release-age gate. This follows the [local installation](https://viteplus.dev/guide/local-cli) and [monorepo](https://viteplus.dev/guide/monorepo) guides.

Oxlint enables TypeScript-aware checks, React and accessibility rules. Explicit security rules retain the previous checks against raw HTML injection, unprotected blank-target links and script URLs (including non-JSX string usage). React Compiler-specific ref, purity and effect-state restrictions are not enabled because the app has not adopted the compiler. The tag-over-role preference is also disabled: our live status containers and labelled SVGs deliberately use ARIA. Rules for accessible names, roles, keyboard interaction and hook usage remain enabled, alongside the real-browser accessibility checks.

Panda, Playwright and Node's existing domain-test runner remain in place. Go retains `gofmt` and `go vet`; Wrangler still packages Cloudflare Workers. pnpm remains the package manager, and the existing Make targets still orchestrate the full application.

Existing `make check-app` invokes the dashboard check, and `make verify-canvas` invokes the canvas check, so both use Vite+ in the existing CI gates. `make verify-app` adds the production build and real API/browser journeys with disposable PostgreSQL; follow the root instructions for its Docker requirements. Tests covering navigation or authentication belong in the existing Playwright suite.

## Panda CSS conventions

The setup follows the [Panda Vite guide](https://panda-css.com/docs/get-started/vite): `@pandacss/vite` precedes React in `vite.config.ts`, generates styles and watches for changes. Both presets are explicit. There is no extra PostCSS integration or separate CSS watcher. `src/global.css` declares the standard layer order; the plugin supplies the generated stylesheet.

Use semantic colour roles (`fg`, `surface`, `border`, `brand`, etc.) in UI code so light and dark themes stay aligned. Add raw palette values to the token definition, not repeated component styles. `strictPropertyValues` catches invalid enumerated CSS values. We have not enabled `strictTokens`: the current UI still uses legitimate raw sizes, and forcing a token for every value before the design is settled would add noise.

Follow Panda's [recipe guidance](https://panda-css.com/docs/recipes/overview): start with `css` for a one-off style, use colocated `cva`/`sva` when a component needs variants, and promote shared design-system recipes into configuration when they have real reuse. Keep style values statically extractable; select complete style objects rather than construct class names or token strings dynamically. Use CSS variables for values known only at runtime.

The [cascade-layer rules](https://panda-css.com/docs/styling/cascade-layers) matter: unlayered CSS outranks Panda's layers. React Flow and CodeMirror adapters intentionally remain scoped, unlayered CSS beside their components so they can override the libraries' styles. Keep that exception limited to vendor internals; ordinary app styling belongs in Panda. The adapters use Panda's token variables rather than a second palette.
