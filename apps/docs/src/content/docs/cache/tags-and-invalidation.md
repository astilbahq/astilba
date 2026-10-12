---
title: Invalidate cached data
description: Mark dependent values stale, make them unreadable, or invalidate one contextless public key.
---

In Astilba Cache, tags describe what a value depends on. Invalidation advances tag watermarks, so readers can reject matching entries without scanning or deleting every stored value.

The application changes its source of truth; Cache only changes whether stored representations may be served. See [Cache fundamentals](/docs/cache/core-concepts/) for keys, tags, Registry, and Bus.

## Choose soft or hard invalidation

| Operation | Effect |
| --- | --- |
| <code>expire()</code> — soft | Values born before the new soft watermark become stale. An eventual read may return the old value after attempting a refresh for a later read. |
| <code>delete()</code> — hard | Values born before the new hard watermark become unreadable wherever that invalidation is visible. Grace cannot resurrect them. |

~~~ts title="invalidation.ts"
const productTag = compound("product", productId)

// Mark every entry carrying the tag stale.
await cache.expire({ tag: productTag })

// Make every entry carrying the tag unreadable.
await cache.delete({ tag: productTag })
~~~

:::caution[Change the source first]
Invalidating a cached representation does not update or remove the underlying record. Apply the source-of-truth change before issuing a hard invalidation so a refill cannot reproduce the old value.
:::

## Choose a tag builder

Use <code>t</code> when the final tag is already in the public tag grammar: lowercase letters, digits, <code>:</code>, <code>_</code>, <code>-</code>, <code>.</code>, <code>|</code>, and <code>%</code>. It rejects an empty value, a reserved <code>__</code> prefix, off-grammar characters, and values over 256 UTF-8 bytes.

~~~ts
const productTag = t`product:${productId}`
~~~

Interpolated values are not escaped. A space, slash, uppercase letter, or other off-grammar character throws <code>InvalidTagError</code> rather than being silently rewritten.

Use <code>compound()</code> when positional structure matters. It escapes percent signs and the <code>:</code> and <code>|</code> delimiters, then prefixes the vector's arity. Delimiter-like values, empty strings, and vectors of different lengths therefore remain distinct.

~~~ts
const productTag = compound("product", productId)
const categoryListingTag = compound("category", categoryId, "listing")
~~~

:::caution[Current <code>compound()</code> validation boundary]
The current source encodes <code>%</code>, <code>:</code>, and <code>|</code> but does not run <code>t</code>'s remaining character or 256-byte validation over compound parts. Keep every part lowercase and grammar-safe, and keep the encoded result within the byte budget. This is a current implementation boundary, not permission to use arbitrary raw identifiers as tags.
:::

Caller-supplied tags on <code>getOrSet()</code> and <code>getOrSetEntry()</code> are rejected when they begin with <code>__</code>. The kernel reserves that prefix for its per-key and per-namespace tags. The <code>Tag</code> brand prevents ordinary raw-string selectors; do not bypass it with a type assertion.

## Add dependencies discovered by the factory

Call-level <code>tags</code> are the initial dependency set. A running factory can refine that set:

~~~ts
factory: async (ctx) => {
  const product = await loadProduct(productId, ctx.signal)
  ctx.setTags([compound("product", product.id)])
  ctx.dependsOn(compound("category", product.categoryId))
  return product
}
~~~

<code>setTags()</code> authors a replacement for the call-level base. <code>dependsOn()</code> adds membership independently and is never erased by a later <code>setTags()</code>. At factory settlement, Cache deduplicates and validates the combined set, stores it with the entry, and uses it for invalidation and write-back fencing.

The final entry may carry at most 126 distinct user tags, whether they came from the call or factory. Reserved tags that Cache adds internally do not consume that user allowance. Invalid, reserved, or excessive factory tags fail the fill before anything is stored.

Dependency declaration closes when the factory promise settles. A later <code>dependsOn()</code> or <code>setTags()</code> call through a retained context throws <code>FactorySettledError</code>. <code>FactoryCtx.dependsOn(tag, { l3: false })</code> is also not implemented and throws; use the render collector's <code>l3: false</code> option only for render-only dependencies.

## Invalidate by key carefully

The key selector maps a user key to Cache's reserved per-key tag:

~~~ts
await cache.expire({ key: `product:${productId}` })
await cache.delete({ key: `product:${productId}` })
~~~

In the current kernel, that selector resolves the **contextless public** canonical key: it has no request or scope input. It does not target principal-derived or tenant-scoped variants of the same user key. Use a dependency tag when data can exist in more than one scope.

The selector types also expose <code>scope</code> on tag invalidation, but the current implementation does not apply it when resolving the Registry tag. Treat a tag purge as affecting every cached entry carrying that tag; do not rely on scope-qualified tag invalidation yet.

## Clear a namespace

<code>clear()</code> bumps the calling instance's namespace version and issues a hard invalidation for the reserved namespace tag. The version makes old keys unreachable on that instance; the hard watermark makes other readers reject pre-clear entries as the invalidation reaches them.

## Understand the result

<code>expire()</code>, <code>delete()</code>, and <code>clear()</code> return a <code>PurgeResult</code> with an epoch, <code>matchedHint</code>, <code>flushed()</code>, and <code>edgePurged()</code>.

In the current preview:

- <code>matchedHint</code> is always <code>"unknown"</code>;
- <code>flushed()</code> resolves immediately without measuring mirror acceptance;
- <code>edgePurged()</code> resolves immediately without invoking a CDN queue;
- the <code>cdn</code> option on <code>delete()</code> is not wired.

Do not use those completion fields as rollout or takedown guarantees yet.

## Configuration boundary

The purge verbs require a <code>Registry</code>. For reads to observe coordinated invalidation, configure Registry, Bus, and L2 together. L2 remains required for fills and lets suspect readers recover through durable deltas and snapshots.

The source Workers factory wires the Coordinator Registry, tick-redialed Durable Object Bus, KV mirror, and request-driven recovery carrier. The CDN path and real purge-completion promises are still not implemented.

When React Router render collection is active, a served entry's stored user tags can also become a <code>Cache-Tag</code> response header. Scope and budget checks run before emission, and reserved tags are filtered. This response tagging does not make the CDN purge path operational; see [Cache HTTP responses](/docs/cache/response-caching/).

## Related

- [How Cache works](/docs/cache/how-it-works/) follows invalidation through live delivery and recovery.
- [Consistency and resilience](/docs/cache/consistency-and-resilience/) explains how reads treat stale or unknown knowledge.
- [Read and cache values](/docs/cache/reading-and-filling/) explains the complete factory lifecycle.
- [Cache HTTP responses](/docs/cache/response-caching/) distinguishes stored dependencies from response emission.
- [Implementation status](/docs/cache/api-status/) records the current purge-result and selector limitations.
