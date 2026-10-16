---
name: Astilba UI
description: Compact, border-led interface primitives with quiet monochrome surfaces and precise state.
colors:
  canvas-dark: "#121212"
  canvas-light: "#fdfdfd"
  ink-dark: "#ffffff"
  ink-light: "#0d0d0d"
  muted-dark: "rgba(202, 202, 202, 0.7)"
  muted-light: "#6c6c6c"
  signal-dark: "oklch(0.72 0.17 32)"
  signal-light: "oklch(0.55 0.19 32)"
  primary-dark: "#ffffff"
  primary-light: "#0d0d0d"
  on-primary-dark: "#0d0d0d"
  on-primary-light: "#ffffff"
typography:
  heading:
    fontFamily: "Inter Variable, ui-sans-serif, sans-serif"
    fontWeight: 500
  body:
    fontFamily: "Geist Variable, ui-sans-serif, sans-serif"
    fontWeight: 400
  mono:
    fontFamily: "JetBrains Mono Variable, ui-monospace, monospace"
    fontWeight: 400
rounded:
  control: "0px"
  elevated: "0px"
  inline-code: "6px"
spacing:
  control-default-inline: "10px"
  control-large-inline: "14px"
  elevated-inset: "16px"
components:
  button-primary:
    backgroundColor: "{colors.primary-dark}"
    textColor: "{colors.on-primary-dark}"
    rounded: "{rounded.control}"
    padding: "0 10px"
    height: "32px"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.muted-dark}"
    rounded: "{rounded.control}"
    padding: "0 10px"
    height: "32px"
  button-large:
    rounded: "{rounded.control}"
    padding: "0 14px"
    height: "40px"
  icon-button-large:
    rounded: "{rounded.control}"
    padding: "0"
    height: "40px"
    width: "40px"
---

# Design System: Astilba UI

## Overview

**Creative North Star: "Quiet Precision"**

Astilba UI is a compact, border-led system that recedes behind the product task. Its nearly monochrome surfaces, square geometry, restrained motion, and exact alignment create hierarchy without decorative noise. A warm signal accent appears only when focus or meaningful state needs to be unmistakable.

The library owns semantic theme roles and common control behaviour. Consumers still own their reset, typography scale, layout, content, and application architecture.

**Key Characteristics:**

- Compact controls with explicit 32px and 40px size contracts
- Flat, square geometry instead of rounded card language
- Semantic light and dark roles rather than consumer-facing raw palettes
- Quiet default states with clear hover, active, disabled, and keyboard focus
- Static CSS with no runtime styling engine

## Colors

Dark mode is the default; light mode is activated with `data-theme="light"` on the document element. The public contract is the semantic CSS variable, while the values below describe its current expression.

### Primary

- **Signal ember** (`oklch(0.72 0.17 32)` dark, `oklch(0.55 0.19 32)` light): reserved for focus and meaningful state.

### Neutral

- **Dark canvas** (`#121212`) and **light canvas** (`#fdfdfd`): page foundations.
- **Dark ink** (`#ffffff`) and **light ink** (`#0d0d0d`): primary content.
- **Muted dark ink** (`rgba(202, 202, 202, 0.7)`) and **muted light ink** (`#6c6c6c`): secondary labels and resting controls.
- **Primary action** (`#ffffff` dark, `#0d0d0d` light): the one high-contrast action surface.

**The Signal Rarity Rule.** Warm color communicates focus or state; it is not ambient decoration.

## Typography

- **Display Font:** Inter Variable with a system sans-serif fallback
- **Body Font:** Geist Variable with a system sans-serif fallback
- **Monospace Font:** JetBrains Mono Variable with a system monospace fallback

The package supplies families and weights as tokens, not a global typographic scale. Compact controls and labels use the body family; labels are `0.8125rem` at weight 500, while text-entry controls are `0.875rem` at weight 400. Consumers choose their own reading and display hierarchy.

## Layout

Controls are dense by design: default controls are 32px high and large controls are 40px high. Default inline padding is 10px, large inline padding is 14px, and icon-only controls remove inline padding while retaining their square size. Use logical CSS properties so the contract holds in either inline direction.

The package does not own containers, grids, breakpoints, or page composition. The showcase demonstrates the system; it does not make its editorial layout part of the public component contract.

## Elevation & Depth

Interfaces are flat by default. Borders and tonal surface changes establish most hierarchy. Elevated surfaces use the semantic `shadows.elevated` role only when an element genuinely leaves the page plane, such as a tooltip or overlay.

**The Flat-by-Default Rule.** Do not add a shadow to compensate for weak grouping; first use spacing, structure, and the correct semantic surface.

## Shapes

Controls, elevated surfaces, and code blocks are square. Inline code is the narrow exception, using a 6px radius to distinguish a small textual token without introducing rounded-card language.

## Components

### Buttons

- **Shape:** square, 32px default height or 40px large height.
- **Primary:** high-contrast action surface with inverse ink.
- **Outline:** transparent surface with the semantic control border.
- **Ghost:** transparent at rest with muted ink.
- **Hover / Active:** quiet semantic surface changes; primary actions remain high contrast.
- **Focus:** a 2px signal outline drawn inside the control.
- **Disabled:** non-interactive with a default cursor and 50% opacity; each appearance remains recognizable.

### Icon Buttons

- **Shape:** square with equal block and inline dimensions.
- **Content:** the accessible name is mandatory and remains independent of the visible glyph.
- **Sizing:** consumers size the glyph; the showcase uses 16px in default controls and 18px in large controls.

### Tooltips

- **Surface:** semantic elevated background, elevated shadow, compact 6px by 8px padding.
- **Motion:** 150ms exponential ease-out on entry and 50ms on exit.
- **Reduced motion:** removes the transition without removing the state change.
- **Feedback:** idle and active labels share one grid area so copy feedback never shifts layout.

### Menus

- **Surface:** square elevated surface with a 4px inset and semantic elevated shadow.
- **Items:** 40px minimum block size, 12px inline padding, 13px body text, and optional trailing content.
- **Input modality:** pointer hover uses only the quiet highlight surface; keyboard navigation adds the signal focus outline.
- **Behaviour:** roots are non-modal by default, ordinary items close after activation, and persistent actions opt out explicitly.
- **Motion:** 250ms scale-and-fade entry and 150ms exit, removed under reduced-motion preferences.
- **Ownership:** the primitive owns accessible menu behaviour and shared item structure; product-specific trigger appearance, widths, selected states, badges, and responsive density stay with the consumer.

### Dialogs

- **Surface:** square elevated surface with a 16px inset and semantic elevated shadow over the semantic scrim, up to 32rem wide.
- **Structure:** `Dialog.Title` names the dialog and an optional `Dialog.Description` explains it.
- **Behaviour:** modal by default; focus moves into the popup, stays there while open, and returns to the trigger on close. Escape and backdrop clicks dismiss.
- **Motion:** 250ms scale-and-fade entry and 150ms exit, removed under reduced-motion preferences.
- **Ownership:** the primitive owns modal behaviour and the shared surface; consumers own content, actions, and alternative sizes such as full-viewport viewers.

### Scroll Areas

- **Feedback:** an optional block-axis fade reflects measured overflow at each edge.
- **Scrollbar:** hidden at rest and revealed by root hover, focus within, or active scrolling.
- **Focus:** the measured viewport joins the native tab order only while it can scroll and receives the shared signal outline.
- **Direction:** roots default to left-to-right coordinates and opt into right-to-left coordinates explicitly.
- **Accessibility:** reduced motion removes transitions; forced colours remove the mask and keep the scrollbar visible.
- **Ownership:** the primitive owns overflow feedback and scrollbar interaction. Consumers own dimensions, content layout, overscroll policy, and position persistence.

### Tabs

- **Structure:** the primitive owns tab and panel associations, roving focus, selection, and disabled states through Base UI.
- **Activation:** selection is manual by default; consumers may activate on focus when every panel is immediately available.
- **Indicator:** a flat semantic selected surface follows the active tab using measured position and size.
- **Motion:** the indicator changes position and size over 250ms with the shared exponential ease-out curve.
- **Accessibility:** arrow keys, Home, End, Enter, Space, focus visibility, right-to-left geometry, forced colours, and reduced motion remain supported.
- **Ownership:** consumers own labels, panel content, surrounding layout, orientation, and controlled state.

### Form Fields

- **Structure:** labels, descriptions, errors, inputs, and textareas are thin native wrappers rather than an application form framework.
- **Sizing:** text inputs are 44px high; textareas begin at 112px and resize along the block axis.
- **Associations:** consumers provide stable IDs, `htmlFor`, and `aria-describedby`; errors may stay mounted, but their ID joins `aria-describedby` only while active.
- **States:** semantic field borders and surfaces cover rest, hover, focus, invalid, readonly, disabled, placeholder, and browser autofill.
- **Accessibility:** native submission, reset, constraint validation, label activation, and forced-colour adaptation remain intact.
- **Ownership:** consumers own validation rules, form state, submission, field layout, and error timing.

## Do's and Don'ts

### Do:

- **Do** use semantic roles such as `ink.muted`, `surface.hover`, and `border.control`.
- **Do** preserve native button and anchor semantics.
- **Do** test the computed browser result, not only generated class names.
- **Do** give every interaction an intentional hover, focus, active, disabled, and reduced-motion state where applicable.
- **Do** add components only after repeated product use proves a shared contract.

### Don't:

- **Don't** expose a raw color scale as the public styling API.
- **Don't** add radii, shadows, or warm accent as generic decoration.
- **Don't** route anchors through the Base UI button primitive.
- **Don't** make product layout, routing, or application state part of this package.
- **Don't** rely on source-order assumptions without a browser-level contract test.
