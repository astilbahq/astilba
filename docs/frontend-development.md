# Frontend development

The production dashboard lives in `apps/platform`. It uses React, TanStack Router/Query, Panda CSS and the generated OpenAPI client. Experiments remain separate. The rationale for the organisation is in [engineering principles](engineering-principles.md) and [agentic development research](agentic-development-research.md).

## Source boundaries

`src/router.tsx` composes routes, session gates and layouts. Features may use TanStack's public navigation components; they must not import our router instance or route definitions. Route-specific state is passed to a feature as typed inputs and callbacks. Shared session query options live in `src/api/session.ts`, alongside the API client.

Keep a feature's components, styling and private helpers together. A single file is sufficient until responsibilities merit a directory. Promote code to a neutral shared module only when another consumer needs it. Avoid a growing catch-all utilities directory or barrels that obscure dependency direction. Existing top-level feature files can move as they are substantially changed; a bulk rename is not a prerequisite for UI work.

`src/styles.ts` holds shared layout styles; new feature-only styles belong beside the feature. `src/theme.ts` supplies runtime theme state. The build-time theme lives in `theme/` and is consumed by `panda.config.ts`:

- `theme/tokens.ts`: primitive values and semantic roles, including light/dark values.
- `theme/global-styles.ts`: document defaults and shared accessibility rules.
- `theme/recipes/`: shared control recipes and their size/appearance variants.
- `src/components/`: native React control primitives backed by those recipes.
- `src/assets/`: source brand assets, including the original Astilba SVG lockup.

Generated `styled-system/` output is ignored by Git. `src/api/generated/` is generated from `contracts/platform`; do not hand-edit or format it.

## Password recovery

`/reset` asks for the account email and confirms the request without revealing whether the account exists. The email opens `/reset?token=…`; the typed route passes the token to `features/auth/password-reset.tsx`, which shows only the new-password field. The real API validates expiry and single use on submission. The only secondary action is “Back to sign in”; users can use the password recovery action there to request another reset email. The reset feature uses the shared auth layout and control recipes, retains failed drafts, and focuses the heading when it advances.

## Workspace invitations

The workspace invitation email opens `/invitation?token=…`; the sign-in page does not offer a separate invitation entry action. The typed route passes the token into `features/auth/invitation.tsx`; there is no code input. New accounts provide name and password, while signed-in accounts accept directly. Missing-token visits direct the user to their email. The sign-in return allowlist includes `/invitation` so an existing account can authenticate and return to the same token URL. Other auth routes, APIs and external destinations remain excluded. Hosted-beta signup and its separate invitation code are unchanged.

## Local commands

Use the Node, pnpm and Go versions pinned in the repository, plus a disposable local Docker daemon. From the repository root:

```sh
pnpm install --frozen-lockfile
export PLAT_APP_DOCKER_HOST=unix:///var/run/docker.sock
pnpm dev
```

Select your actual local Docker Unix socket; the example above is the usual Linux path. `pnpm dev` builds the Go API, starts pinned PostgreSQL on a free loopback port, applies migrations and starts the dashboard with hot reload at `http://127.0.0.1:54182/login`. Sign in with `owner@example.test` / `correct horse battery staple`; the initial workspace is **UI Sandbox**. This is a development-only account. Restart the command after changing Go code.

Local email is printed as plaintext in the `pnpm dev` terminal, including the recipient, subject and full body. Request a password reset for `owner@example.test`, then open the link from that output. The launcher explicitly selects console delivery and supplies a local sender; it sends no SMTP mail. Console delivery requires non-hosted development with a loopback listener and HTTP browser origin. Other environments retain SMTP delivery, with no console fallback.

`pnpm dev --port 55182` uses 55182 for the UI and 55181 for the API. Separate checkouts have separate databases; choose different ports and **separate browser profiles** when using them together. Browser cookies are shared across ports on `127.0.0.1`, so opening two previews in one profile replaces their sign-in sessions. Inherited `ASTILBA_*` configuration is cleared so the preview uses its own database and development settings. Stop it before `make verify`, whose browser fixture uses the default ports.

Ctrl-C stops the API, frontend and owned database container while retaining demo data. `pnpm dev --reset` deletes **this checkout's** demo database and seeds the same initial account/workspace. Generated credentials and keys stay in ignored, private `.artifacts/dev`; both Vite development servers deny HTTP access to `.artifacts`. Keep the state directory with its container: do not copy it to another checkout or remove the keyring from a database you intend to retain.

An atomic `.artifacts/dev.lock` prevents two launchers from changing the same database. If a forced kill leaves it behind, inspect `launcher` for the parent PID and ports, and `processes` for child process-group IDs. Use `ps` and a port-listening tool to establish which recorded processes still belong to this launcher (PIDs may have been reused). Stop those owned groups, confirm the old API/UI listeners and launcher are gone, then remove **only** `.artifacts/dev.lock` and restart. A dead parent PID alone does not prove its children stopped. Normal Ctrl-C removes the lock automatically.

The package's `prepare` hook generates Panda's types during installation. If lifecycle scripts were disabled, run `pnpm --filter @astilba/dashboard prepare` explicitly. For a frontend-only process against an already running local API, use `pnpm --filter @astilba/dashboard dev`; its defaults remain UI 54182 / API 54181. See the application setup guide for running the packaged service.

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

The setup follows the [Panda Vite guide](https://panda-css.com/docs/get-started/vite): `@pandacss/vite` precedes React in `vite.config.ts`, generates styles and watches for changes. Both presets are explicit; `jsxFramework: "react"` enables extraction of recipe variants from React component props. There is no extra PostCSS integration or separate CSS watcher. `src/global.css` declares the standard layer order; the plugin supplies the generated stylesheet.

Use semantic colour roles (`fg`, `surface`, `border`, `brand`, etc.) in UI code so light and dark themes stay aligned. Add raw palette values to the token definition, not repeated component styles. `strictPropertyValues` rejects invalid enumerated CSS values, and `strictTokens` requires tokens for token-backed properties. Shared spacing, typography, sizes and radii use the Panda scale or named theme tokens. A bracketed raw value is an explicit exception for a one-off effect; do not use it to bypass the token system.

Use logical properties: `marginBlockStart`, `paddingInline`, `inlineSize`, `borderInlineEnd`, and `textAlign: "start"`. Vite+ extends Panda's `no-physical-properties` rule with the shared logical-property policy for production component styles, and the `logical-properties` Panda plugin rejects physical properties in global styles and recipes at config loading. These checks run through the existing `check`/`lint` commands. The vendor adapters use logical dimensions; CodeMirror's cursor border remains a physical library geometry override.

Panda's official lint plugin is loaded through the local `oxlint-panda.mjs` wrapper so the root and package commands resolve the same app config. It also checks token paths, raw colours, invalid nesting, extraction and missing source coverage. The JSX accessibility rules map our primitives to their native elements.

Follow Panda's [recipe guidance](https://panda-css.com/docs/recipes/overview): start with `css` for a one-off style, use colocated `cva`/`sva` when a component needs variants, and promote shared design-system recipes into configuration when they have real reuse. Keep style values statically extractable; select complete style objects rather than construct class names or token strings dynamically. Use CSS variables for values known only at runtime.

The [cascade-layer rules](https://panda-css.com/docs/styling/cascade-layers) matter: unlayered CSS outranks Panda's layers. React Flow and CodeMirror adapters intentionally remain scoped, unlayered CSS beside their components so they can override the libraries' styles. Keep that exception limited to vendor internals; ordinary app styling belongs in Panda. The adapters use Panda's token variables rather than a second palette.

## Inline auth validation

`Form validation={authValidation.signIn}` opts a form into Base UI’s Form/Field validation. Sign-in/MFA, both reset forms and invitation acceptance use it; other Form callers retain native constraint validation, including raw controls outside Field. Migration is explicit because disabling browser validation on an unregistered native control would lose its validation.

`components/form-validation.ts` adapts synchronous Zod schemas to Base UI validators. Synchronous checks block submission; API requests stay in the existing mutation path. Field renders Base UI Label/Error with the shared recipes, keeping PasswordInput’s reveal button and label action order intact. Base UI owns invalid state, descriptions, first-invalid focus, submit-first timing and revalidation. Native constraint failures use the same schema-generated messages as custom rules. Server field errors are stable memoized maps, clear on edits, and use the same inline presentation; general failures use one form alert. Preserve drafts on all failures.

Auth validation lives in `features/auth/validation.ts`. English copy lives separately in `features/auth/messages.ts`; `createAuthValidation(messages)` accepts complete messages and named interpolation functions for a later localisation layer. Do not use global Zod locale configuration, concatenate translated fragments, or classify server errors by English text. Error codes select messages. This migration does not add a locale selector or translate existing screen copy.

New passwords count Unicode code points (15–128) rather than HTML/JavaScript UTF-16 length. Passwords are submitted verbatim; the client does not trim or normalize them. Sign-in checks presence only, so a new-password policy does not block existing credentials. The Go API/OpenAPI contract remain authoritative; Zod provides early feedback rather than a security boundary. Add focused browser coverage for validation/focus/draft behavior and real API outcomes when migrating further forms.

## Shared controls

Import primitives directly from `src/components/button`, `input`, `password-input`, `select`, `textarea`, `checkbox`, `label`, or `link`. They preserve native DOM props, events and React 19 refs. Use `Field` and `BusyButton` from `src/ui.tsx` when form error association or submission feedback is needed. Field’s `labelAction` renders after the complete input, including PasswordInput’s reveal button, so related actions do not interrupt consecutive fields in tab order. Its recipe uses grid placement to retain the action beside the label.

```tsx
<Button variant="primary" size="md" type="submit">Save changes</Button>
<Button variant="secondary" type="button">Cancel</Button>
<Button variant="danger" type="button">Delete project</Button>
<Field name="email" label="Email" type="email" variant="quiet"
  placeholder="you@example.com" autoComplete="username" />
<Label htmlFor="region">Region</Label>
<Select id="region" name="region">...</Select>
<Link to="/login" search={{ returnTo: "/servers" }} variant="plain">Sign in</Link>
<Anchor href="https://example.com" target="_blank" rel="noreferrer">Documentation</Anchor>
```

`PasswordInput` composes the shared Base UI-backed Input and ghost IconButton, inspired by [Chakra’s password input](https://chakra-ui.com/docs/components/password-input). It starts masked; the labelled Show/Hide button supports pointer, Enter and Space activation. Pointer activation preserves the input focus/selection, while keyboard focus stays on the toggle. Native value/defaultValue, change events, refs, names, autocomplete and error attributes reach the input. Spellcheck, autocorrection and auto-capitalization stay disabled when revealed. Password values are not copied into component state or persisted. The `passwordInput` slot recipe reserves logical end padding for the 40px target and 16px icon. Root hover sets the enabled input’s `--input-hover-border-color` to `control.interactiveBorder`, keeping the field highlighted over the reveal button. Input’s base border uses that optional override before the variant’s `--input-border-color`, avoiding a conflict with Panda’s higher-priority recipe variant layer. Explicit focus/error/system-colour borders retain priority, and the existing border transition handles entry and exit. Use `Field inputComponent={PasswordInput}` to retain label/error association; login opts in, other password forms remain unchanged.

Button variants are `primary`, `secondary`, `danger`, and `ghost`. Input, Select and Textarea support `outline` and `quiet`; the latter needs visible control content (for example a readable placeholder on an otherwise empty input). Control sizes are `sm` and `md`. `size` on Input/Select is the recipe size, not the native HTML character/row count.

Tune appearance in `theme/tokens.ts` and `theme/recipes/`, not route selectors. `control.border`, `control.quietBorder`, and `control.interactiveBorder` own field boundaries; the interactive role is shared by hover and focus. Focus adds a 1px outline at zero offset to the 1px border without changing layout. Explicit `aria-invalid` errors retain the error edge; untouched required inputs do not appear invalid. Buttons use `action.*` and `destructive.*` roles, with a variant-matched focus ring. Disabled controls ignore hover; forced-colour and reduced-motion modes retain accessible states.

AuthFrame scopes the colour roles to the accepted auth palette. Recipes consume the same roles inside and outside authentication, and auth layout styles contain no input or button overrides. Checkbox remains a native checkbox with a shared recipe.

`Label` uses the label slot of the shared `field` recipe; `Field` consumes its root and header slots. This follows [Chakra's Field slot structure](https://github.com/chakra-ui/chakra-ui/blob/main/packages/react/src/theme/recipes/field.ts), with the existing grid spacing retained. Labels support both `htmlFor` and wrapping a control. Layout overrides such as checkbox rows remain explicit classes.

`Link` is the styled anchor passed to [TanStack Router's `createLink`](https://tanstack.com/router/latest/docs/framework/react/guide/custom-link), preserving route/search/parameter inference, refs, active state and navigation behavior. Use `Anchor` for external URLs, downloads and same-document anchors. Both share the `link` recipe, with `radii.xs` (2px) rounding the base focus outline and `radii.control` (6px) retained for navigation: `underline` is the existing default, `plain` underlines on hover, and `navigation` preserves sidebar padding and active styling. These adapt [Chakra's Link recipe](https://github.com/chakra-ui/chakra-ui/blob/main/packages/react/src/theme/recipes/link.ts) while retaining inline wrapping and current dimensions. `link.fg` is scoped by AuthFrame for auth text; other links keep their existing text colour. `link.focus` and `auth.focus` alias `control.interactiveBorder`, sharing neutral keyboard outlines across auth and the application. The root `.skip-link` sets `--colors-link-fg` to the shared `fg` token, so its text stays neutral outside AuthFrame; its outline still comes from the link recipe and its background from `surface`. Keep typography and placement in layout classes, and link interactions in the recipe. Accessibility lint recognizes both primitives and route destinations supplied through `to`; external-link checks also cover the wrappers.


### Base UI and appearance

Custom interactive controls use [Base UI](https://base-ui.com/react/overview/accessibility), with Panda owning styles. `Button` and `Input` render Base UI primitives while retaining their native props and refs. Specify `type="submit"` on submit buttons; Base UI defaults to `button`. Native `Label`, `Anchor`, `Select`, `Textarea`, and `Checkbox` retain browser accessibility and form semantics. `Link` continues to use TanStack typed routing. Use Base UI for new compound widgets instead of implementing keyboard navigation or focus management by hand.

`IconButton` reuses the button recipe's square shape, with a 40px default target and a required `aria-label`. Its default `ghost` variant has a quiet hover fill and a foreground-coloured focus ring. Icons are decorative SVGs; the button's accessible name supplies its meaning.

`Tooltip` takes a `content` prop and a single element child, and composes through Base UI's `render` prop. Keep `TooltipProvider` at the application root with `delay={300}` for a shared 300ms hover delay. Keyboard focus opens the tooltip immediately. Tooltip appearance uses `radii.full`, the menu’s `surface`/`fg` roles, `fontSizes.secondary` (13px), and `shadows.menu`. Its fade/0.98-scale transition uses `durations.tooltip.open` (150ms), `durations.tooltip.close` (50ms), and `easings.out`, with no additional CSS delay. Base UI’s starting/ending attributes and `--transform-origin` handle lifecycle and placement; `data-instant`, reduced motion, and theme capture suppress transitions. Closed tooltips ignore pointer input; forced colours restore a system-colour border. The trigger still needs its own accessible name; the repeated visual tooltip is hidden from assistive technology. Keep tooltip content supplementary and non-interactive. `MenuRoot`, `MenuTrigger`, `MenuContent`, `MenuItem`, `MenuRadioGroup`, and `MenuRadioItem` provide shared dropdown parts. Menu and tooltip use Panda slot recipes, following [Chakra's menu anatomy](https://github.com/chakra-ui/chakra-ui/blob/main/packages/react/src/theme/recipes/menu.ts) with Base UI state attributes. MenuContent requires an accessible label and renders its portal as a named region. Radio items close on selection by default; ThemeMenu sets `closeOnClick={false}` to keep appearance choices open while switching. Positioners handle viewport collision; keyboard navigation, dismissal, and focus restoration come from Base UI.

`ThemeMenu` uses these components for Light, Dark, and System. System is the default and follows media-query changes live. The harmless preference persists under the existing `astilba-theme` key; earlier Light/Dark values remain valid. Blocked browser storage only limits persistence. The resolved theme continues to drive the document, CodeMirror and React Flow. Auth places the trigger at the top inline-end; the dashboard places it above the main content. Auth keeps Appearance before the main content in DOM/tab order, matching its top-of-page position. The skip link bypasses it. Sign-in then tabs through Email, Password, Show/Hide password, Forgot password, and Sign in; recovery stays visually beside the password label. Use natural DOM order, with no positive tabindex or automatic field focus. Auth passes its palette class to the trigger and portal popups so they retain auth colours and typography outside the main element.


Menu motion lives in the shared `menu` slot recipe. It adapts the supplied Transitions.dev scale-and-fade pattern to [Base UI’s animation lifecycle](https://base-ui.com/react/handbook/animation): `data-starting-style` enters from 0.97 scale, `data-ending-style` exits to 0.99, and `data-closed` disables pointer events. The popup’s `--transform-origin` follows Base UI positioning and viewport flips. `durations.menu.open` (250ms), `durations.menu.close` (150ms), and `easings.menu` own timing. Base UI retains the popup until the transition completes; no application timers or animation dependency are needed. The existing global reduced-motion rule disables the transition.

Menu geometry uses Panda scale tokens: `radii.xl` (12px), item `radii.lg` (8px), `spacing.1.5` (6px popup padding), `fontSizes.secondary` (13px), `spacing.2` (8px icon gap), `sizes.4` (16px icons), and `sizes.8` (32px minimum item height). The shared Icon accepts its contextual size through `--icon-size`, with a 20px default for other uses. `colors.menu.itemHover` and `shadows.menu` own both themes. Normal themes have no popup border; forced colours use a system-colour border.

Theme changes use the [View Transition API](https://developer.mozilla.org/en-US/docs/Web/API/Document/startViewTransition) for one snapshot crossfade with `durations.theme` (250ms) and `easings.theme` (ease-in-out). `ThemeProvider` temporarily disables component transitions before committing the complete requested appearance, so snapshots capture resolved colours together, including React-controlled editor themes. Normal timings resume when the crossfade finishes. Pending preference and system changes are kept together so superseded callbacks cannot discard a manual choice. Initial rendering, unchanged resolved themes, reduced motion and unsupported browsers switch immediately.

Button and IconButton share a 200ms colour hover transition (`durations.button`, `easings.out`). IconButton defaults to ghost; `colors.action.ghostHover` provides its lighter hover/open fill. Theme transitions temporarily take precedence, and reduced motion disables both.

Neutral `page`/`surface`, `fg` and `subtle` roles share the light/dark palette throughout the app, including auth aliases: #fdfdfd/#121212 backgrounds, #0d0d0d/#ffffff text, and #6c6c6c/rgba(202, 202, 202, 0.7) muted text. Action/contrast, feedback and field-border roles remain separate.

Primary `action.solid`, `action.contrast` and `action.hover`, plus `control.border`, `control.quietBorder` and `control.interactiveBorder`, use neutral greys in both themes. Auth aliases these shared roles so input and primary-button states stay aligned. Dark primary actions use #ffffff at rest and #e8e8e8 on hover, retaining #0d0d0d text and a focus ring matching the active fill. Brand, non-auth link and feedback roles remain independent; auth links continue to follow the auth action colour.

Menu items share button hover timing (`durations.button`, 200ms; `easings.out`) for their highlighted background. The transition applies in both directions; focus outlines remain immediate. Global reduced-motion and theme-capture overrides apply to items too.

Field hover and focus share a softer neutral interactive edge: #8c8c8c in light mode and #707070 in dark mode. The zero-offset focus outline retains its existing thickness. Input, Select and Textarea border transitions share the button and menu-item timing (`durations.button`, 200ms; `easings.out`) in both directions, with the existing reduced-motion and theme-capture overrides.

Focus outlines appear immediately in the final interactive or error colour; only the field border animates. Animating outline colour from `outline: none` would briefly expose the inherited text colour on focus.
