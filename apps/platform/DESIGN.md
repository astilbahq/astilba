---
name: "Astilba Authentication & Shared Controls"
description: "Aligned stack authentication and reusable Base UI and native controls backed by Panda recipes."
colors:
  auth-fg: "#0d0d0d"
  auth-fg-dark: "#ffffff"
  auth-subtle: "#6c6c6c"
  auth-subtle-dark: "#cacacab3"
  auth-surface: "#fdfdfd"
  auth-surface-dark: "#121212"
  auth-page: "#fdfdfd"
  auth-page-dark: "#121212"
  auth-border: "#dedede"
  auth-border-dark: "#3a3a3a"
  auth-control-border: "#898989"
  auth-control-border-dark: "#858585"
  auth-action: "#0d0d0d"
  auth-action-dark: "#ffffff"
  auth-on-action: "#ffffff"
  auth-on-action-dark: "#0d0d0d"
  auth-action-hover: "#2b2b2b"
  auth-action-hover-dark: "#e8e8e8"
  auth-soft: "#edf1f5"
  auth-soft-dark: "#263442"
  auth-focus: "#8c8c8c"
  auth-focus-dark: "#707070"
  auth-selection: "#d6e4f3"
  auth-selection-dark: "#334d67"
  auth-danger: "#a72b35"
  auth-danger-dark: "#ff7b7b"
  fg: "#0d0d0d"
  fg-dark: "#ffffff"
  surface: "#fdfdfd"
  surface-dark: "#121212"
  soft: "#e8efe8"
  soft-dark: "#2b3c30"
  menu-item-hover: "#f4f4f5"
  menu-item-hover-dark: "#ffffff0a"
  control-border: "#898989"
  control-border-dark: "#858585"
  control-quiet-border: "#dedede"
  control-quiet-border-dark: "#3a3a3a"
  control-interactive-border: "#8c8c8c"
  control-interactive-border-dark: "#707070"
  action-ghost-hover: "#f4f4f5"
  action-ghost-hover-dark: "#ffffff0a"
  action-solid: "#0d0d0d"
  action-solid-dark: "#ffffff"
  action-contrast: "#ffffff"
  action-contrast-dark: "#0d0d0d"
  action-hover: "#2b2b2b"
  action-hover-dark: "#e8e8e8"
  destructive-solid: "#a72b35"
  destructive-solid-dark: "#ffadb5"
  destructive-contrast: "#ffffff"
  destructive-contrast-dark: "#401219"
  destructive-hover: "#8c202c"
  destructive-hover-dark: "#ffc3c9"
  error: "#982332"
  error-dark: "#ff7b7b"
  link-fg: "#275e49"
  link-fg-dark: "#9ad8b4"
  link-focus: "#8c8c8c"
  link-focus-dark: "#707070"
typography:
  headline:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, sans-serif"
    fontSize: "24px"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "-0.025em"
  body:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.6
  label:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, sans-serif"
    fontSize: "14px"
    fontWeight: 500
    lineHeight: 1.6
  input:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, sans-serif"
    fontSize: "16px"
    fontWeight: 400
    lineHeight: 1.6
  action:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, sans-serif"
    fontSize: "14px"
    fontWeight: 600
    lineHeight: 1.6
  secondary:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.6
  input-sm:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.6
  control-action:
    fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 600
    lineHeight: 1.6
  control-input:
    fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "16px"
    fontWeight: 400
    lineHeight: 1.6
  control-input-sm:
    fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.6
  control-body:
    fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.6
  control-label:
    fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 500
    lineHeight: 1.6
rounded:
  link: "2px"
  control: "6px"
  menu: "12px"
  menu-item: "8px"
  pill: "9999px"
spacing:
  2: "8px"
  3: "12px"
  4: "16px"
  5: "20px"
  6: "24px"
  7: "28px"
  10: "40px"
  12: "48px"
  18: "72px"
  24: "96px"
components:
  icon-button:
    textColor: "{colors.fg}"
    rounded: "{rounded.control}"
    width: "40px"
    height: "40px"
  menu:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.secondary}"
    rounded: "{rounded.menu}"
    padding: "6px"
  tooltip:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.secondary}"
    rounded: "{rounded.pill}"
    padding: "6px 10px"
  button-primary:
    backgroundColor: "{colors.action-solid}"
    textColor: "{colors.action-contrast}"
    typography: "{typography.control-action}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  button-primary-hover:
    backgroundColor: "{colors.action-hover}"
  button-secondary:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.control-action}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  button-secondary-hover:
    backgroundColor: "{colors.soft}"
  button-danger:
    backgroundColor: "{colors.destructive-solid}"
    textColor: "{colors.destructive-contrast}"
    typography: "{typography.control-action}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  button-danger-hover:
    backgroundColor: "{colors.destructive-hover}"
  input-outline:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.control-input}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  input-quiet:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.control-input}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  password-input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.control-input}"
    rounded: "{rounded.control}"
    padding: "8px 48px 8px 12px"
  select:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.control-input}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  textarea:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.control-input}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  checkbox:
    width: "18px"
    height: "18px"
  recovery-link:
    rounded: "{rounded.link}"
    textColor: "{colors.auth-action}"
    typography: "{typography.secondary}"
  field-error:
    textColor: "{colors.error}"
    typography: "{typography.secondary}"
  field-label:
    textColor: "{colors.fg}"
    typography: "{typography.control-label}"
  link-underline:
    rounded: "{rounded.link}"
    textColor: "{colors.link-fg}"
    typography: "{typography.control-body}"
  link-plain:
    rounded: "{rounded.link}"
    textColor: "{colors.link-fg}"
    typography: "{typography.control-body}"
  link-navigation:
    typography: "{typography.control-body}"
    rounded: "{rounded.control}"
    padding: "8px 10px"
  error-notice:
    textColor: "{colors.error}"
    typography: "{typography.control-body}"
    rounded: "{rounded.control}"
    padding: "12px 16px"
  auth-content:
    textColor: "{colors.auth-fg}"
    width: "100%"
  auth-button-primary:
    backgroundColor: "{colors.auth-action}"
    textColor: "{colors.auth-on-action}"
    typography: "{typography.action}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
---

# Design System: Astilba Authentication & Shared Controls

## Overview

**Creative North Star: "Aligned stack"**

A familiar, focused entry to Astilba: a compact form on an open surface, with a shared starting edge and an unmistakable primary action. The owner selected Aligned stack. Consistent grouping, quiet sign-in field outlines and visible focus supply structure without an outer card.

The auth visual direction covers AuthFrame and its sign-in, MFA, reset, invitation and conditional hosted-signup flows. Shared control recipes also apply across the application; auth supplies local palette overrides rather than separate control styling. The signed-in dashboard retains its existing green identity. Recording shared control defaults does not approve or redesign its other styles. Product truth remains in ../../PRODUCT.md; the auth system font and logo lettering remain provisional. The auth logo is temporarily omitted at the owner’s request; the heading starts the column.

**Key Characteristics:**

- Open neutral light or dark auth surface with a narrow aligned column.
- Reusable Base UI and native controls with semantic variants and consistent focus states.
- Persistent busy and error feedback with native form behaviour.

## Colors

Shared backgrounds are #fdfdfd in light mode and #121212 in dark mode; text is #0d0d0d and #ffffff respectively. Muted text is #6c6c6c in light mode and rgba(202, 202, 202, 0.7) in dark mode. Auth inherits these neutral roles. `auth-*` frontmatter entries record that scoped palette; unsuffixed semantic roles record shared control defaults. `-dark` names each dark counterpart. Sidecar ramps are preview interpolations, not additional application tokens.

### Primary

`action.solid`, `action.contrast` and `action.hover` own primary buttons; `destructive.*` own destructive buttons. Primary buttons share neutral action tokens: #0d0d0d with white text in light mode, #ffffff with #0d0d0d text in dark mode; hover fills are #2b2b2b and #e8e8e8. Auth aliases these same roles. Destructive roles remain shared. `error` owns invalid edges and notices; within auth it resolves to `auth.danger` in both themes. Dark-mode error text and invalid edges use #ff7b7b, with 7.47:1 contrast on the #121212 surface; light-mode values are unchanged.

`link.fg` owns link text; auth uses its action colour and other application links retain their brand colour. `link.focus` and `auth.focus` both alias `control.interactiveBorder`, giving every shared link a neutral grey keyboard outline (#8c8c8c light / #707070 dark). The root skip link overrides only its link text role with `fg`, retaining the shared outline and `surface` background outside the auth palette scope.

### Neutral

`surface`, `fg` and `soft` supply control fill, text and secondary hover. `control.border` and `control.quietBorder` supply normal and quiet idle boundaries. `control.interactiveBorder` supplies the shared grey field hover and focus edge, stronger than the quiet idle border in both themes, with a softer neutral edge for hover and focus. Auth's page semantic remains declared, but its visible canvas and current controls use the surface role. Focus Blue remains for auth links, and Selection Blue remains auth text selection.

**The Auth Boundary Rule.** Share neutral background, foreground and muted roles throughout the app; AuthFrame scopes its action and field palettes. Shared control recipes use semantic roles; field interaction edges use grey throughout, primary actions are neutral throughout, while brand and non-auth link colours remain green.

**The Control State Rule.** Let recipes own idle, hover, focus and invalid states. Input variants set the idle border variable; the base recipe owns the border so interaction states remain effective.

## Typography

The auth headline remains 24px/600 with line-height 1.3. Its system sans stack (`fonts.ui`) and logo lettering are provisional; Apfel Grotezk is excluded. Shared controls inherit the surrounding font: `fonts.body` outside auth and `fonts.ui` inside it. The frontmatter records those resolved contexts.

Labels use medium weight; actions use semibold. Medium inputs use 16px text, small inputs use 14px; both button sizes use 14px. Secondary auth text remains 13px. Typography, dimensions, radii and spacing are registered theme tokens or Panda scale values.

## Layout

Auth remains a centred grid with a minimum viewport block size (`100dvh`) and a 360px maximum content inline size. It has no inner card padding. Page padding is 80px block-start, 28px inline and 72px block-end; at `sm` (40rem), these become 96px, 24px and 96px. The top inline-end appearance button sits 16px from the edges on small screens and 24px at `sm`; reserved block-start space keeps tall forms clear of it. Tall forms grow and scroll naturally. The logo is temporarily omitted at the owner's request; the heading starts the column.

Content groups use 24px; forms use 20px; multi-form sections retain 28px. Labels sit 8px above fields. Secondary actions use a 12px gap and 4px block-start margin. Alignment follows the writing direction through logical properties. Exploratory slider settings remain excluded.

## Elevation & Depth

The auth canvas is continuous. Alignment and spacing separate groups. Floating menus use the theme-aware `shadows.menu` token: a light layered shadow in light mode, and deeper ambient shadow with subtle inset highlights in dark mode. Focus is a state indicator, not elevation.

**The Open Surface Rule.** Group auth content through alignment and spacing, with no outer card padding, border, radius or shadow.

## Shapes

Controls use the registered control radius and uniform one-pixel borders. The content wrapper has no border, radius or shadow. Medium and small control sizes are minimum block sizes of 46px and 40px, not fixed heights; content can expand them.

## Components

Shared controls live in `src/components/`, backed by `theme/recipes/`. Use the [frontend development guide](../../docs/frontend-development.md#shared-controls) for imports, props and styling policy. The recipes own interactions; AuthFrame only scopes their palette.

### Buttons

Primary, secondary, danger and ghost variants share logical padding, size variants and disabled behaviour. Primary/danger hover changes both fill and border; their focus colour follows the current variant fill, including while hovered. Secondary focus follows foreground text. Focus-visible is a 2px outline with 2px offset. Button and IconButton hover transitions use `durations.button` (200ms) and `easings.out` for fill, border, text and outline colour. Ghost hover/open uses #f4f4f5 in light mode and 4% white in dark mode. Theme switches temporarily synchronize these colours with the shared 250ms timing. BusyButton reads “Working…” without moving; disabled controls use 0.55 opacity and ignore hover.

### Appearance, icon buttons and popups

Appearance offers Light, Dark and System as a single-selection menu with a visible checkmark. System follows OS changes live; explicit choices persist across reloads. The trigger icon reflects the selected preference. Auth and signed-in layouts share ThemeMenu. Choosing a theme keeps this menu open; Escape, outside interaction or the trigger dismisses it.

IconButton uses the shared Base UI button with a square shape, a 40px default target and a required accessible name. Its ghost variant stays transparent at rest and uses `action.ghostHover` on hover/open, with a foreground-coloured focus ring. Menu and Tooltip use Base UI for interaction and Panda slot recipes for styling. The menu is at least 176px wide with 12px corners, 6px padding, 13px text and a theme-aware shadow instead of a border. Items have a 32px minimum height, 8px corners, 16px icons, an 8px icon gap and 4px/12px block/inline padding; they can grow for enlarged or wrapped text. Highlighted items use `menu.itemHover`: #f4f4f5 in light mode and 4% white in dark mode. Item highlights fade in and out using the same 200ms `durations.button` and `easings.out` tokens as buttons. Only the background transitions; keyboard focus retains an immediate 2px inset grey outline. Forced-colours mode restores a system-colour popup border because shadows are suppressed. The menu portal supplies a named region. The root TooltipProvider sets a shared 300ms hover delay; keyboard focus remains immediate. The tooltip is pill-shaped (`radii.full`) and shares the menu’s surface, foreground, 13px type and theme-aware shadow. It fades and scales from 0.98 over 150ms, then closes over 50ms with no extra delay (`durations.tooltip.open` / `close`, `easings.out`). Base UI owns placement and transition completion; keyboard focus, instant dismissal/group transitions and reduced motion skip the animation. Forced colours restore a system-colour border. The tooltip supplements the trigger label; its repeated visual text is hidden from assistive technology. Auth palette classes explicitly reach portal popups. Portalled menu and tooltip positioners use viewport-fixed positioning so resizing cannot leave a stale horizontal scroll area in WebKit.

The menu scales from 0.97 to 1 while fading in over 250ms, then scales to 0.99 while fading out over 150ms. Both use `cubic-bezier(0.22, 1, 0.36, 1)`. Timing lives in `durations.menu.*` and `easings.menu`; origin follows the positioned anchor. Closing menus ignore pointer input, interrupted transitions remain continuous, and reduced-motion mode is immediate.

Theme changes use one 250ms snapshot crossfade (`durations.theme`, `easings.theme`) through the browser View Transition API. Component transitions are temporarily disabled so backgrounds, text, portals and editor themes are captured together without trailing colour animations. Normal hover timings return when the crossfade finishes. Initial rendering, unchanged resolved themes, reduced motion and browsers without the API switch immediately. Explicit choices and live System changes share this path; a newer change cancels stale pending updates.

### Inputs / Fields

Input uses Base UI and retains native input events and labels. Select, Textarea, Checkbox, Label and anchors retain native browser semantics. Input, Select and Textarea share `outline` and `quiet` variants, with `outline`/`md` defaults. Sign-in explicitly uses `quiet` with readable placeholders; variant selection does not depend on a placeholder selector. Quiet controls need visible content for discoverability. Select keeps native behaviour; Textarea resizes in the block direction.

Enabled hover uses the interactive border. Border colour transitions in both directions using the shared button/menu timing: `durations.button` (200ms) and `easings.out`. Reduced motion disables the transition, and theme changes retain the global transition override. The focus outline appears immediately in its final colour, avoiding a flash from the browser’s default outline colour. Focus adds a 1px outline of the same colour at zero offset to the 1px border, appearing as one 2px edge without shifting layout. Explicit `aria-invalid="true"` retains the error border on hover and the error border/outline on focus. Untouched required controls are not styled as errors. Field continues to associate labels and error descriptions.

### Form validation

Sign-in (including MFA), password reset and invitations opt into the shared Form’s Base UI validation mode. It uses Base UI Form/Field/Label/Error with synchronous Zod field validators. Other forms keep native validation until migrated. Validate on submit, focus the first invalid control, then revalidate as users correct values. Inline errors use the field recipe’s error slot, semantic error colour and 13px secondary type; errors span the field width below its control. Base UI associates errors with controls and owns invalid state. Form-level API failures use one alert; individual errors have no competing live regions.

Authentication rules and messages are separate. A message catalogue is passed into createAuthValidation; complete messages and named interpolation parameters can be replaced by a future locale without global Zod state. Server feedback maps machine error codes, not English API prose. The sign-in password is only checked for presence. New passwords count 15–128 Unicode code points to match the API and are never trimmed or transformed before submission. Native required/type/autocomplete semantics remain; Base UI suppresses browser validation popups and the schema supplies application-language messages. Failed submissions retain drafts and the Go API remains authoritative.

### Password input

PasswordInput composes Input and a ghost IconButton using the `passwordInput` root/input/toggle slots. It starts masked and exposes Show password / Hide password through an accessible button and matching tooltip. The input reserves 48px logical end padding for a 40px target with a 16px eye icon; its existing quiet/outline and sm/md styles remain available. Pointer activation preserves editing focus and selection; keyboard users can Tab to the toggle and activate it with Enter or Space. Hovering anywhere inside PasswordInput, including the reveal toggle, keeps its enabled input’s hover border active. The recipe supplies a separate hover-border override consumed by Input ahead of its variant’s idle border, so Panda’s variant layer cannot reset it. Explicit focus, error and forced-colour edges retain priority. Disabling the input also disables its toggle. Revealing changes only the native type; value, autocomplete, validation, name, refs and errors remain on the same input. Spelling/correction/capitalization are disabled. The sign-in Field uses this control; other password forms keep their current input.

### Labels

Label renders a native label using the `field` slot recipe. Its grid layout, 6px gap and medium weight preserve both standalone and wrapping-label layouts. The same recipe owns Field’s root (8px gap), label, control and action layout. A label action follows the full control in DOM order; the hasAction grid variant places it beside the label. Explicit `htmlFor` associations and controls nested inside labels retain native click and keyboard behaviour. Checkbox rows keep their existing layout classes.

### Links

Anchor renders native destinations; Link adds TanStack's typed routing to the same primitive. The `link` recipe owns colour, cursor, underline and focus. `underline` preserves the existing default; `plain` underlines on hover; `navigation` preserves sidebar padding and active-route treatment. Links keep inline text wrapping and a 4px underline offset. Keyboard focus uses a neutral 2px outline at 3px offset, using the shared interactive-border token. The base link uses `radii.xs` (2px) to soften outline corners; the navigation variant retains its 6px control radius. The skip link uses foreground text and the same outline in both themes. Recovery, secondary actions and brand links keep their existing layout and typography classes.

### Checkbox

Checkbox preserves the native input at 18px square, with the brand accent and native checked/keyboard behaviour. It retains the inherited focus-visible outline rather than the text-input focus treatment.

### Containers and navigation

AuthFrame retains the aligned column and familiar flow grouping. Recovery stays beside the password label; secondary actions align to the start edge. Native tab order keeps the credential fields together: Skip to content, Appearance, Email, Password, Show/Hide password, Forgot password, Sign in. The top-corner Appearance control stays before the main content; activating the skip link makes the next Tab reach Email. Field renders its labelAction after the complete control and uses grid placement for the label-row position, without positive tabindex. Reset and invitation secondary links share that starting edge; signup back links remain centred. Links keep their 2px focus outline with 3px offset. Hosted signup remains conditional.

Password recovery shows one stage at a time: account email, a privacy-preserving check-email confirmation, then the new-password form opened from `/reset?token=…`. The code stays out of the form. The field label is “New password”, without a requirements list. Reset uses the same quiet fields and start-aligned secondary links as sign-in. The only secondary action is “Back to sign in”. Invalid links show an inline error, failed submissions retain the password draft, and completion confirms the change before returning to sign-in. Stage transitions focus the heading.

Workspace invitations open `/invitation?token=…`; sign-in has no invitation prompt. Without a token, the page directs users to their invitation email. New accounts enter their name and password in quiet fields; signed-in users confirm acceptance without re-entering credentials. The sign-in link preserves the invitation URL so existing accounts can return after authentication. Successful acceptance removes the form, focuses “Workspace joined”, and offers “Open projects”. Invalid links show an inline error and failed submissions retain the draft.

### Feedback and access modes

ErrorNotice consumes the semantic error colour with a matching border and `role="alert"`; progress remains inline status text. Reduced motion removes transitions. Forced-colour fields use ButtonText boundaries and Highlight focus; buttons retain system-colour boundaries and focus. Native checkbox accessibility remains intact.

## Do's and Don'ts

### Do:

- **Do** reuse Button, Input, Select, Textarea, Checkbox, Label, Link and Anchor, with Field and BusyButton for form behaviour.
- **Do** use registered tokens, semantic colour roles and logical CSS properties for control styling.
- **Do** preserve native labels, keyboard interaction, autofill, error association, reduced motion and forced-colour states.
- **Do** keep hosted-only actions conditional and preserve the same auth identity on self-hosted installations.
- **Do** treat the auth interface font and logo lettering as provisional.

### Don't:

- **Don't** apply the auth palette to the dashboard as an automatic consistency fix.
- **Don't** style native inputs or buttons through route descendant selectors; change shared recipes or semantic tokens.
- **Don't** use quiet empty controls without visible content such as a readable placeholder.
- **Don't** use Apfel Grotezk, restore the outer auth card or introduce dark input underlines.

Input borders share neutral roles across auth and the app: quiet #dedede/#3a3a3a, outlined #898989/#858585, and hover/focus #8c8c8c/#707070 (light/dark). The focus outline uses the same interactive colour as hover, with no offset. Primary button focus continues to follow its current fill.
