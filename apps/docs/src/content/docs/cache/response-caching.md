---
title: Cache HTTP responses
description: Emit safe Cache-Tag headers from React Router renders without sharing private content.
---

Astilba Cache can connect value-cache dependencies to a shared HTTP cache. During a React Router request, the middleware records every Cache entry the render consumes, checks whether all of those entries are public, and emits their user tags as a <code>Cache-Tag</code> response header when the result is eligible.

:::caution[Tagging is implemented; CDN purging is not]
The React Router collection and response-header path is implemented in the current source preview. The application still owns its <code>Cache-Control</code> policy, and Cache does not yet send tag purges to Cloudflare or another CDN. Do not treat <code>edgePurged()</code> as proof of an edge purge.
:::

## Understand the division of responsibility

The adapter answers one question: **is it safe to associate this response with the dependencies Cache observed?** It does not decide that the response should be shared.

| Responsibility | Owner |
| --- | --- |
| Set <code>public</code>, <code>s-maxage</code>, or another shared-cache directive | Your application or framework response policy |
| Record Cache hits and fills used by the render | Astilba Cache and the React Router middleware |
| Reject a response that consumed non-public or unreadable scope | The middleware |
| Assemble an eligible <code>Cache-Tag</code> header | The middleware |
| Purge the CDN after <code>delete()</code> | Not implemented in the current source |

The middleware never writes <code>public</code> or <code>s-maxage</code>. If your application sets no <code>Cache-Control</code>, it fills in <code>private</code>. To opt an eligible route into shared caching, set the final response policy yourself, for example:

~~~http
Cache-Control: public, s-maxage=60
~~~

That opt-in remains subject to the middleware's safety gate. If the render later consumes a private dependency, the middleware overwrites the policy with <code>Cache-Control: private</code> and removes <code>Cache-Tag</code>.

## Let the middleware collect dependencies

Register <code>cacheMiddleware()</code> at the React Router root, then use the request Cache from <code>cacheContext</code>. The middleware opens one render collector before loaders run and commits it after <code>next()</code> returns.

This setup targets Cloudflare Workers. In another runtime, omit <code>waitUntil</code> or pass that runtime's equivalent lifecycle hook.

~~~tsx title="root.tsx (Cloudflare Workers)"
import { waitUntil } from "cloudflare:workers"
import { cacheMiddleware } from "@astilba/cache/react-router"
import {
  authenticatedUserContext,
  authMiddleware,
} from "./auth.server"
import { cache } from "./cache.server"

export const middleware = [
  authMiddleware,
  cacheMiddleware({
    cache,
    waitUntil,
    request: ({ context }) => {
      const user = context.get(authenticatedUserContext)
      return { userId: user.id, tenant: user.tenantId }
    },
  }),
]
~~~

Each served Cache hit contributes the entry's complete stored tag set and stored scope evidence. Each successful fill contributes its final tag set and the scope the kernel resolved for it. Reserved per-key and per-namespace tags remain internal and are filtered before header emission.

Pass <code>currentRequest()</code> into each read so scope resolution sees the authenticated identity frame. Obtain the request Cache through <code>cacheContext</code> rather than importing the construction-time instance into route code:

~~~ts title="routes/product.ts"
import { t } from "@astilba/cache"
import {
  cacheContext,
  currentRequest,
} from "@astilba/cache/react-router"
import type { Route } from "./+types/product"

export async function loader({ context, params }: Route.LoaderArgs) {
  const cache = context.get(cacheContext)
  const productId = params.productId

  const product = await cache.getOrSet({
    key: `product:${productId}`,
    request: currentRequest(),
    scope: "public",
    tags: [t`product:${productId}`],
    factory: ({ signal }) => loadProduct(productId, signal),
  })

  return { product }
}
~~~

An explicit public scope is a claim that identity cannot change the value. The development request guard helps detect reads through <code>ctx.request</code>, but it cannot inspect values captured in closures. See [Control cache sharing](/docs/cache/scopes-and-privacy/).

## Read the three response outcomes

| What the render consumed | Response behavior |
| --- | --- |
| No managed dependency | Preserve the application's <code>Cache-Control</code> and <code>Cache-Tag</code>. If no cache policy exists, add <code>Cache-Control: private</code>. |
| Only eligible public dependencies | Preserve the application's cache policy, replace <code>Cache-Tag</code> with the collected user tags, and default to <code>private</code> only when no policy exists. |
| Any ineligible dependency | Force <code>Cache-Control: private</code>, remove <code>Cache-Tag</code>, and emit one <code>l3_ineligible</code> telemetry event. |

A response becomes ineligible for one of four reasons:

- <code>scope</code> — a managed dependency was tenant- or principal-scoped;
- <code>scope-unreadable</code> — a served entry did not expose readable scope metadata;
- <code>late-tag</code> — a required tag arrived after header commit;
- <code>budget</code> — the tag list exceeded its configured count or byte budget.

One unsafe dependency poisons a mixed render. Marking a tag <code>l3: false</code> never hides the associated entry's scope: it can suppress a purge tag, but it cannot make private response content public.

## Build a custom response adapter deliberately

The standalone collector is available to authors of another response adapter. It can declare a dependency that is not backed by a Cache entry and lets the adapter apply the returned decision itself:

~~~ts
const collector = cache.collect()
collector.dependsOn(t`site:theme`)
const emission = collector.commitHeaders({
  maxBytes: 16 * 1024,
  maxTags: 1000,
})
~~~

A bare <code>RenderCollector.dependsOn()</code> has no stored entry and therefore makes no scope claim. That absence is caller-trusted input, not evidence that the dependency is public. A custom adapter must independently establish that the rendered data is safe to share; when its scope is private or unknown, fail closed to a private response and omit its tags. Never use a bare declaration to hide a managed private dependency. Use <code>{ l3: false }</code> to keep a verified render-only tag out of the emitted header and out of timing and budget checks.

The React Router middleware owns a different request-bound collector internally; it does not expose that collector for route code to mutate. Creating a standalone collector inside a React Router request does not add tags to the middleware's eventual header. Use the automatic Cache hit/fill collection there, or write a custom adapter that declares and commits its own dependencies.

This is different from <code>FactoryCtx.dependsOn()</code>. Factory-declared tags persist into the cached entry and automatically carry that fill's scope into the request collector. <code>FactoryCtx.dependsOn(tag, { l3: false })</code> currently throws <code>NotImplementedError</code> because the stored entry format cannot preserve a per-tag emission flag for later hits.

## Stay within the header budget

The React Router adapter defaults to <code>L3_BUDGET_DEFAULT</code>:

- <code>maxBytes: 16 * 1024</code> for the comma-joined header value;
- <code>maxTags: 1000</code> collected tag occurrences.

Override the limits with <code>cacheMiddleware({ l3Budget })</code>. Budgeting counts every collected occurrence and the comma separators. After an eligible decision, the adapter deduplicates tags before writing the actual header, so it may emit fewer bytes than it measured but never more.

Cloudflare documents the current <code>Cache-Tag</code> syntax and aggregate response-header limit in [Purge cache by cache-tags](https://developers.cloudflare.com/cache/how-to/purge-cache/purge-by-tags/). Astilba's defaults match the 16 KB and approximately 1,000-tag guidance; they do not implement the purge request itself.

## Account for immutable responses

Some platform responses have immutable header guards, including redirect and fetch-originated responses. If direct header mutation fails, the middleware rebuilds the response with the same body, status, status text, and existing headers, then applies the decided posture. Network-error responses that cannot be rebuilt are returned untouched; they expose no usable cache headers.

## Know the remaining boundary

Safe dependency collection, scope demotion, budget enforcement, and <code>Cache-Tag</code> emission exist today. The configured <code>Cdn</code> capability, <code>delete({ cdn })</code>, and <code>PurgeResult.edgePurged()</code> do not yet drive or await a real CDN purge. Until that path exists, response tags are useful for inspection and future integration but are not end-to-end invalidation support.

Continue with [React Router](/docs/cache/react-and-server-apps/) for complete middleware setup, [Inspect cache behavior](/docs/cache/observability/) for ineligibility telemetry, or [Implementation status](/docs/cache/api-status/) for the preview ledger.
