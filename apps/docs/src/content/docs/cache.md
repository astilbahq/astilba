---
title: Cache
description: A portable server-side TypeScript cache with explicit invalidation, resilience, and privacy boundaries.
tableOfContents: false
prev: false
next: false
---

Astilba Cache stores the result of expensive server-side work so later calls can reuse it. It is designed for TypeScript applications that need explicit control over invalidation, failures, and who may share a cached value.

> **Development preview:** `@astilba/cache` is not published to npm, the Cache repository is not publicly accessible, and there is no supported production installation path.

Cache is a result cache, not a source of truth. Your application still owns the database or upstream service, chooses which values may be shared, and invalidates cached representations after a successful mutation.

## Choose your next step

| Goal | Start here |
| --- | --- |
| Decide whether the design fits | [Overview](/docs/cache/overview/) |
| Check exactly what works today | [Implementation status](/docs/cache/api-status/) |
| Review the smallest source example | [Source walkthrough](/docs/cache/quickstart/) |
| Learn the storage and invalidation vocabulary | [Cache fundamentals](/docs/cache/core-concepts/) |
| Evaluate the current Workers composition | [Cloudflare Workers](/docs/cache/cloudflare-workers/) |
| Add request context and response tags | [React Router](/docs/cache/react-and-server-apps/) |

If this is your first visit, read the overview before copying an example. The implementation ledger is authoritative whenever another page appears to promise more than the current source provides.
