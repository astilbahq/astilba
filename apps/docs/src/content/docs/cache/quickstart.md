---
title: Source walkthrough
description: Follow one Astilba Cache miss and hit through the implemented memory Store in the reviewed source snapshot.
---

This walkthrough follows one product through the smallest configuration in the reviewed source snapshot. The first read calls the factory; the second reuses the stored result.

:::caution[Source example, not a runnable public quickstart]
<code>@astilba/cache</code> is not available from npm, and the Cache repository is not publicly accessible. Use this page to understand the reviewed implementation. It is not a public checkout or production installation recipe.
:::

## Understand the setup

The portable constructor requires explicit capabilities:

| Part | Purpose here |
| --- | --- |
| <code>createCache()</code> | Creates the application-facing cache. |
| <code>memory()</code> | Supplies a bounded, per-instance Store. The source example uses it as a development L2 because a factory fill currently requires L2. |
| <code>Clock</code> | Supplies logical time to the portable kernel. |
| <code>Rng</code> | Supplies randomness without hard-coding a platform source into the kernel. |
| Factory | Loads the product when no stored value is usable. |

No Registry or Bus is configured, so this example demonstrates read, fill, and reuse—not distributed invalidation.

## Follow one product read

~~~ts title="cache.ts"
import { createCache, memory, t } from "@astilba/cache"
import type { Clock, Rng } from "@astilba/cache"

interface Product {
  id: string
  name: string
}

const clock: Clock = { now: () => Date.now() }
const rng: Rng = { next: () => Math.random() }

// memory() is normally an isolate-local L1. Using it as L2 here gives the
// unreleased kernel the Store it requires without pretending this is durable.
const developmentStore = memory({
  clock,
  maxEntries: 512,
  maxBytes: 5_000_000,
})

const cache = createCache({
  namespace: "storefront",
  clock,
  rng,
  l2: developmentStore,
})

const productId = "sku-123"
let originLoads = 0

async function loadProduct(
  id: string,
  signal: AbortSignal,
): Promise<Product> {
  if (signal.aborted) throw signal.reason
  originLoads += 1
  return { id, name: "Canvas backpack" }
}

const options = {
  key: `product:${productId}`,
  tags: [t`product:${productId}`],
  factory: ({ signal }: { signal: AbortSignal }) =>
    loadProduct(productId, signal),
}

const first = await cache.getOrSet(options)
const second = await cache.getOrSet(options)

console.log(first, second, originLoads) // same product, one origin load
~~~

The first call misses, runs <code>loadProduct()</code>, encodes the result, and writes it to the supplied Store. The second call resolves the same canonical key and reads that stored value. The tag records a dependency you could later invalidate after adding a Registry.

The application-facing portion begins at <code>options</code>: choose a stable key, declare dependency tags, and provide the origin factory. A runtime adapter should own most of the construction above it.

## Know what is and is not exercised

The reviewed source path exercises:

- the implemented <code>memory()</code> Store, including its entry and byte bounds;
- the implemented <code>t</code> tag builder;
- the root <code>createCache()</code> and <code>getOrSet()</code> paths;
- the built-in JSON codec and one in-isolate singleflight identity.

It deliberately leaves out:

- durability or sharing across isolates;
- explicit invalidation, which requires a Registry;
- live invalidation delivery and recovery, which require Registry, Bus, and L2 together;
- elapsed TTL and grace behavior, which remains unfinished even though the options are typed;
- request identity, cross-isolate locks, telemetry, and the React Router response-cache integration.

:::note[Why <code>memory()</code> is passed as L2]
The current kernel refuses a factory fill without an L2 Store. The same small <code>Store</code> contract is used by both tiers, so <code>memory()</code> is sufficient for this source example. It does not become shared or durable simply because it occupies the <code>l2</code> configuration field.
:::

## Move to a runtime integration

- [Cloudflare Workers](/docs/cache/cloudflare-workers/) replaces the manual clock, random source, and development Store with the current Workers factory and drivers.
- [React Router](/docs/cache/react-and-server-apps/) exposes that cache through server middleware and carries request identity to reads.
- [Cache fundamentals](/docs/cache/core-concepts/) explains the storage and invalidation vocabulary.
- [Inspect cache behavior](/docs/cache/observability/) shows how to inspect a cache value with <code>explain()</code> and configure event telemetry.
- [Implementation status](/docs/cache/api-status/) lists every incomplete or provisional surface.
