# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Solo developers and small teams managing applications on their own infrastructure. The interface serves both hosted and self-hosted installations with the same Astilba identity.

## Product Purpose

Astilba provides a visual workspace for deploying and maintaining applications on user-controlled servers. The [product brief](docs/platform-product-brief.md) remains the authority for capabilities, positioning and product scope; this record supplies context for interface design.

## Operating Context

Users return to manage applications and infrastructure. Authentication includes sign-in, MFA, password reset and invitations; hosted signup is conditional on the installation. Preserve those implemented flows and the distinction between hosted and self-hosted capabilities.

## Brand Commitments

Preserve the Astilba name. The supplied logo is stored in `apps/platform/src/assets/astilba-lockup-horizontal-light.svg`, but its lettering is provisional and may change. Do not use Apfel Grotezk for the interface: the owner explicitly rejected that font. The remaining visual direction is open. Existing implementation colours and typography are not binding brand decisions.

For authentication, the owner selected Aligned stack: a compact, left-aligned form on an open surface, with light, uniform input borders and no surrounding card border. Keep familiar controls and restrained styling. Typography remains provisional.

## Capabilities and Constraints

Use the existing React, TanStack Router/Query and Panda CSS foundation. Follow [frontend development](docs/frontend-development.md) and [engineering principles](docs/engineering-principles.md). Product and architecture decisions remain in their existing documents; do not invent promotional claims, account methods or capabilities for a visual composition.

## Accessibility & Inclusion

Follow the repository's WCAG 2.2 AA requirement, including keyboard access, visible focus, reduced motion and responsive layouts. Preserve working password-manager and browser autofill behaviour.
