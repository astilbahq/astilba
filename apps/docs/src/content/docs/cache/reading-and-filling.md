---
title: Read and cache values
description: Choose between the simple value API and the metadata-rich entry API.
---

Astilba Cache provides two read APIs. Use <code>getOrSet()</code> when you only need a value. Use <code>getOrSetEntry()</code> when the caller needs cache metadata or the factory may intentionally skip storage.

If terms such as factory, L1, L2, origin, or entry are new, see [Cache fundamentals](/docs/cache/core-concepts/).

## Choose the return shape

### Return a value

~~~ts
const product = await cache.getOrSet({
  key: `product:${productId}`,
  tags: [compound("product", productId)],
  factory: async ({ signal }) => loadProduct(productId, signal),
})
~~~

The factory receives a <code>FactoryCtx</code> without <code>skip()</code>. A skipped result therefore cannot be mistaken for a successful <code>T</code>.

### Return an entry

~~~ts
const entry = await cache.getOrSetEntry<string>({
  key: "optional-banner",
  factory: async (ctx) => {
    if (!shouldRenderBanner()) return ctx.skip()
    return loadBanner()
  },
})

if (!entry.skipped && entry.value !== undefined) {
  render(entry.value)
}
~~~

<code>getOrSetEntry()</code> returns these fields:

| Field | Meaning in the current kernel |
| --- | --- |
| <code>value</code> | The stored, filled, or stale value; <code>undefined</code> for a miss, skip, or negative entry. |
| <code>tier</code> | <code>l1</code>, <code>l2</code>, <code>origin</code>, or <code>miss</code>. <code>l1.5</code> exists in the type but is not emitted by the current implementation. |
| <code>stale</code> | The returned value was not fresh at this read's consistency level. |
| <code>servedOnError</code> | A classified transient failure reused a stale candidate after serve-time revalidation. |
| <code>durable</code> | Optional evidence from this serve. Every newly filled origin result reports <code>true</code> or <code>false</code>; an L2 hit reports <code>true</code>; a principal-scoped L1 hit reports <code>false</code>. A public or tenant L1 hit omits the field because that serve does not consult L2. |
| <code>skipped</code> | The entry-form factory called <code>ctx.skip()</code>; nothing was stored. |
| <code>age</code> | Whole milliseconds since the served envelope's <code>bornMs</code> fill-start timestamp, measured by the injected Clock and clamped at zero. A miss or skip reports zero. |

Every newly filled origin result includes <code>durable</code>. A <code>false</code> value does not mean the factory failed: it can mean the value was confined to L1 by scope, no L1 existed to retain that private value, a classified transient L2 write failure was suppressed, or an attempted negative was refused to protect a value already in L2. Only a later hit can omit <code>durable</code>; that absence is different from <code>false</code> because the current serve cannot prove either outcome. Treat <code>entry.durable === true</code> as positive evidence and compare explicitly with <code>false</code> only when you need to distinguish known non-durability from an unknown answer.

## Follow the fill lifecycle

1. **Read configured tiers.** Cache tries L1 before L2. It checks the stored codec identity before decoding, then validates the reconstructed entry.
2. **Join compatible work.** Concurrent compatible calls share one in-isolate foreground factory execution.
3. **Run the factory.** The factory receives an <code>AbortSignal</code>, optional request context, and typed failure and dependency helpers. The current kernel creates a fresh signal but does not yet abort it on a cache deadline.
4. **Close the tag set.** When the factory settles, Cache uses the latest <code>setTags()</code> base—or the call-level tags when no replacement was authored—and unions every <code>dependsOn()</code> membership into one validated set.
5. **Fence the result.** A hard invalidation observed during the fill can reject write-back. When verified invalidation knowledge advanced, the kernel can re-mint the birth epoch and refetch within a bounded three-attempt budget.
6. **Write by scope.** Shared scopes may reach L2; principal-derived values are L1-only. A successful fill hydrates L1 when one is configured.

When the bounded attempts still leave no servable value, <code>getOrSet()</code> throws <code>FencedError</code>. <code>getOrSetEntry()</code> reports a non-durable miss instead.

A Store read has its own failure boundary. A structurally classified <code>throttled</code> or <code>unavailable</code> read emits <code>store_read_suppressed</code> and behaves as a miss for serving, allowing another tier or the factory to supply the value. An unclassified read error propagates unchanged. L1 can therefore absorb an L2 outage for keys already present locally; without such an L1 hit, repeated calls may refill while L2 remains unavailable.

:::caution[Protect origin during an L2 outage]
The current kernel has no circuit breaker or origin-load backoff for suppressed Store reads. Before production use, applications must provide their own origin concurrency limits, load shedding, and alert thresholds, and should retain the bounded L1 supplied by the Workers factory. The package remains unreleased partly because these operating thresholds have not yet been established through deployed measurements.
:::

A strong, coordinated miss live-checks the canonical key and namespace tags plus the caller-declared tags before the first factory attempt. That check anchors the fill at the Registry's current global epoch before origin work begins. Factory-discovered tags join the final stored set and the write-back fence; a hard purge delivered for one of them during the fill can fence and retry the result, even though that tag was not individually available to the pre-factory check.

## Declare dependencies during the factory

Use factory helpers when the origin result reveals dependencies that the caller could not know beforehand:

~~~ts
const article = await cache.getOrSet({
  key: `article:${articleId}`,
  tags: [t`articles`],
  factory: async (ctx) => {
    const article = await loadArticle(articleId, ctx.signal)

    ctx.setTags([t`articles`, compound("article", article.id)])
    ctx.dependsOn(compound("author", article.authorId))
    return article
  },
})
~~~

<code>setTags()</code> replaces the call-level <code>tags</code> base; only its most recent call is the authored base. <code>dependsOn()</code> adds memberships independently, so calling it before or after <code>setTags()</code> produces the same union. Cache stores that final set, uses it for later invalidation, and contributes it to an active React Router render collector.

The combined set may contain at most 126 distinct user tags. Invalid, reserved, or excessive factory tags fail the fill before storage. A stashed context cannot mutate a completed value: calling <code>dependsOn()</code> or <code>setTags()</code> after the factory promise settles throws <code>FactorySettledError</code>.

<code>FactoryCtx.dependsOn(tag, { l3: false })</code> currently throws <code>NotImplementedError</code>; the stored format cannot retain that per-tag response-emission flag for later hits. <code>FactoryCtx.setTtl()</code> also throws <code>NotImplementedError</code> while elapsed TTL is deferred.

## Cache an explicit 404

Set <code>notFoundTtl</code> only when an <code>HttpError</code> with status 404 is a cacheable fact:

~~~ts
import { httpError } from "@astilba/cache"

const entry = await cache.getOrSetEntry<Product>({
  key: `product:${productId}`,
  notFoundTtl: "30s",
  factory: async () => {
    const response = await fetchProduct(productId)
    if (response.status === 404) throw httpError(response)
    if (!response.ok) throw httpError(response)
    return response.json()
  },
})

if (entry.tier === "miss") {
  throw new Error("Cache could not establish a result")
}

if (entry.value === undefined) {
  return new Response("Not found", { status: 404 })
}
~~~

Check <code>tier</code> before interpreting <code>undefined</code>: the entry form also uses <code>{ tier: "miss", value: undefined }</code> for a terminal fenced fill, and <code>skip()</code> produces a skipped miss. Neither is a 404 fact.

When serve-time validation completes and the factory-running caller's own stale candidate does not suppress the 404, Cache attempts one negative L2 write and the call resolves with <code>value: undefined</code> at <code>tier: "origin"</code>. Before writing, the L2 negative-write guard checks the current entry in that same Store with the serving path's codec and invalidation rules. A decodable value that is fresh or stale refuses the negative; Cache also refuses when its invalidation verdict cannot be established. The origin result then reports <code>durable: false</code>, the existing L2 value remains stored for later reads to re-evaluate, and <code>neg_suppressed</code> is emitted.

The guard is deliberately Store-local and does not inspect L1. Codec-incompatible, undecodable, or dead L2 bytes do not trigger it; normal newer-envelope arbitration still applies afterward. The current Store contract also makes this a read-then-write check rather than an atomic compare-and-set, so a competing write can interleave after the guard's read.

A classified retryable write failure is a separate <code>durable: false</code> outcome. An accepted negative can supply a later invalidation-fresh L2 hit; that hit also surfaces <code>undefined</code>, while retaining the tier, age, and durability evidence for the entry actually read. <code>notFoundTtl</code> still does not expire the entry by elapsed time. The internal storable placeholder is never exposed as an application value.

The plain <code>getOrSet()</code> form also resolves <code>undefined</code> when an opted-in 404 takes the negative disposition. Its current declaration remains <code>Promise&lt;T&gt;</code>; this is a type limitation, not proof that the result is defined. Use an explicit union such as <code>getOrSet&lt;Product | undefined&gt;(...)</code> whenever you enable <code>notFoundTtl</code>, or use <code>getOrSetEntry()</code> and branch on both <code>entry.tier</code> and <code>entry.value</code>.

Negative entries are stored only in L2. A negative result that reaches the serve path skips L1 hydration and makes a best-effort attempt to delete an older L1 value for the same canonical key unless the L2 negative-write guard refused the write. That cleanup still runs after a classified retryable L2 write failure. Permanent or unclassified Store failures propagate before the result reaches this step. If the delete fails, the call still returns the negative fact, but the older L1 entry can be served by a later read until the tier evicts it or a later value fill overwrites it. A guard refusal leaves both tiers unchanged.

## Compatible concurrent calls

Singleflight joins calls only when their canonical key and structural settings agree. Tags, TTL, grace, negative-cache TTL, resolved scope, codec identity, consistency, and API form all participate. Tag order does not matter because tags are sorted and deduplicated first.

The first compatible call runs the shared factory; later compatible calls wait for that work. A successful fill, or a stale serve already revalidated by the factory-running call, is shared with the metadata for the served entry. If that call had no stale candidate and the factory produced only a classified transient failure, each waiting caller makes its own stale-on-error decision using the candidate it read before joining. The factory-running call can receive the origin error while a waiting caller with an eligible stale value revalidates and serves its own copy. A hard invalidation that lands before that revalidation still prevents the stale serve.

When serve-time validation completes, an opted-in shared 404 has three served dispositions:

- If the factory-running caller declared <code>grace</code> and holds a still-servable stale value, it suppresses the negative write and serves that value. Every compatible joiner inherits the leader's value and evidence, even if that joiner did not read a candidate of its own.
- If that grace-eligible stale candidate revalidates as dead or unknown, the leader makes the negative write attempt and serves that result. Every compatible joiner inherits the negative result; a candidate that cannot be established servable is not eligible for caller-local fallback.
- If the factory-running caller has no grace-eligible candidate, it makes the negative L2 write attempt once inside the singleflight window and shares the not-found fact. Each waiter then compares that fact with its own earlier read. A waiter that declared <code>grace</code> and observed a still-servable stale value returns that value without <code>servedOnError</code>; a waiter without a servable stale candidate returns <code>undefined</code>. When <code>grace</code> is absent from the compatible calls, every caller takes the negative disposition even if it observed a stale value.

Every negative attempt above passes through the same L2 negative-write guard. A refusal changes storage and durability evidence, not the shared not-found disposition: callers taking that disposition still receive <code>undefined</code>, while the existing L2 value remains stored. Serve-time validation may instead throw <code>RegistryUnavailableError</code> under the configured unavailable posture. Waiters never repeat or rewrite the negative entry.

For classified transient failures, Cache consults <code>defaults.staleIfError</code> only for a call that declares <code>grace</code>. Without grace, no caller can use a stale candidate as transient-error fallback; that origin error propagates without invoking the application's classifier. Opted-in 404 handling is the separate fact path described above and can resolve a negative result without grace. This does not change soft-stale eventual refresh: that separate path may still return its stale value as described below.

With <code>dev: true</code>, an incompatible same-key call fails loudly. Otherwise it runs separately and emits <code>singleflight_option_mismatch</code> telemetry.

## Codec changes become misses

Stored values carry a codec identity. Cache checks that identity before decoding, so an unexpected codec becomes a miss instead of a mistyped value. Intentional migrations can allow selected older identities through <code>defaults.acceptCodecs</code>.

The built-in codec is a plain JSON round trip. Use JSON-representable values only: it does not revive dates, classes, functions, or bigint values. Supply a custom <code>Codec</code> with a new identity when you need another wire format; if you accept an older identity, the current decoder must understand those older bytes.

## Current boundaries

:::caution[Time is not enforced yet]
Per-call and default TTL and grace values do not currently expire entries by elapsed time. Stored envelopes use zero TTL and grace fields, and a declared <code>notFoundTtl</code> opts into a negative write without enforcing the requested duration. Entry <code>age</code> is measured from the served envelope's <code>bornMs</code> fill-start timestamp, but that observation does not make timing policy active.
:::

A soft-stale eventual read currently awaits a best-effort refresh, then still returns the stale value for that call. The planned background adoption and retry lifecycle is not implemented, so this path does not yet provide background stale-while-revalidate latency.

<code>ctx.graced</code> is not populated and <code>reuseGraced()</code> throws <code>NotImplementedError</code>. Use the documented stale-on-error path instead of factory-directed grace reuse.

## Related

- [Source walkthrough](/docs/cache/quickstart/) shows both value reads against the implemented memory Store used as a development-only L2.
- [Cache fundamentals](/docs/cache/core-concepts/) explains the storage tiers and read vocabulary.
- [Consistency and resilience](/docs/cache/consistency-and-resilience/) explains when stale values may be reused.
- [Cache HTTP responses](/docs/cache/response-caching/) explains how served and factory-declared tags reach a response collector.
- [Inspect cache behavior](/docs/cache/observability/) shows how to witness the stored final tag set.
- [Implementation status](/docs/cache/api-status/) lists provisional metadata and unimplemented helpers.
