# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Astilba product developers use these packages to build consistent public interfaces without recreating theme, control, and accessibility behaviour in each application. These private workspace packages serve Astilba's monorepo applications; no new npm releases are planned.

## Product Purpose

Astilba UI provides one maintained source for Astilba's semantic design tokens and accessible React controls. Success means a consuming product can share Astilba's theme roles and common control behaviour while retaining ownership of its framework shell, layout, content, and interaction architecture.

## Positioning

The token package exposes the same semantic roles as framework-neutral CSS variables and as a Panda CSS preset. The component package builds on those roles with native semantics, Base UI where behaviour warrants it, and static CSS, so consumers do not inherit a runtime styling engine.

## Operating Context

The packages are developed in a pnpm monorepo and consumed by Astilba's Astro and React surfaces. Consumers import tokens and component styles once, then use either the tree-shakeable root entry or focused component subpaths. Astro consumers may server-render components without hydrating them when native HTML behaviour is sufficient.

## Capabilities and Constraints

- `@astilba/tokens` remains framework-neutral and provides local static CSS plus a Panda CSS preset.
- `@astilba/ui` provides local ESM-only React components, static CSS, and focused component entry points.
- React and React DOM compatibility is defined by the package peer dependencies.
- Components do not provide a global reset, typography system, application layout, router, or client-side state architecture.
- Links that look like buttons remain anchors rather than being passed through the Base UI Button primitive.
- Workspace builds retain package-output, declaration, consumer and accessibility checks without npm publication.

## Brand Commitments

The packages express Astilba's compact, border-led, nearly monochrome interface language through semantic roles rather than a public raw colour scale. The warm signal accent is reserved for focus and meaningful state.

## Evidence on Hand

- `packages/tokens/tests/` verifies the static token contract.
- `packages/ui/tests/` verifies component behaviour, accessibility, compiled CSS, package output, and consumer bundling.
- `apps/ui-showcase/` is the private visual and accessibility harness.

## Product Principles

1. Prefer semantic roles to raw visual values.
2. Preserve native element semantics and accessible interaction behaviour.
3. Keep styling static and consumer module graphs narrow.
4. Add shared primitives in response to proven product use, not speculative breadth.
5. Leave product-specific layout and application architecture with the consumer.

## Accessibility & Inclusion

Target WCAG 2.2 AA. Preserve keyboard operation, visible focus, semantic element choice, sufficient contrast, reduced-motion behaviour, and equivalent light and dark theme states.
