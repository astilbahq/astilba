---
title: Overview
description: Portable infrastructure tools and libraries with explicit correctness boundaries, documented as they exist today.
tableOfContents: false
---

Astilba builds portable infrastructure tools and libraries with explicit correctness boundaries. These docs describe each released or reviewed surface, what its guarantees cost, and which parts remain incomplete.

The products are at different lifecycle stages. Env is a 0.3 public alpha; Cache is a source-reviewed development preview. Read each product's status page before treating an example as a supported setup.

## Products

### [Env](/docs/env/)

A local-first configuration contract compiler with explicit lifecycles and physically separated browser and server projections.

Start with the [overview](/docs/env/overview/), [configure a Node application](/docs/env/quickstart/), choose a [runtime or platform](/docs/env/nodejs/), or check the exact [release and support](/docs/env/release-and-support/) boundary for the 0.3 public alpha.

### [Cache](/docs/cache/)

A portable server-side TypeScript cache with explicit invalidation, resilience, and privacy boundaries.

Start with the [overview](/docs/cache/overview/), check the [implementation status](/docs/cache/api-status/), review the [source walkthrough](/docs/cache/quickstart/), or learn about the source adapters for [Cloudflare Workers](/docs/cache/cloudflare-workers/) and [React Router](/docs/cache/react-and-server-apps/).

:::caution[Cache is a development preview]
`@astilba/cache` is not available from npm and has no supported production installation path. The implementation ledger records the exact reviewed source snapshot and incomplete release gates.
:::

## Read the docs in context

Each product owns its own version tree and release boundary:

- For Env, [Release and support](/docs/env/release-and-support/) records the 0.3 public-alpha package, runtime, exports, and framework boundary.
- For Cache, [Implementation status](/docs/cache/api-status/) is the authoritative ledger for the reviewed source snapshot, partial behavior, placeholders, and missing release gates.

Guides show an outcome, concepts explain one mental model, and reference pages enumerate the public surface. If an example and a status ledger ever appear to disagree, follow the ledger and report the stale page.

## For agents

Fetch the [agent setup prompt](/docs/agent-setup/prompt.md) to connect a supported coding agent to Astilba's public documentation tools. The prompt configures documentation access only; it does not install a product.

Use the public [MCP server](/docs/agents/mcp/) directly to search and read the same published Markdown corpus through a read-only protocol endpoint.
