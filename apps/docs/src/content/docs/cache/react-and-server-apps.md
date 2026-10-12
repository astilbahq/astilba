---
title: React Router
description: Provide Astilba Cache to React Router v8 loaders and actions while carrying server request identity safely.
---

The current source exposes server middleware at <code>@astilba/cache/react-router</code>. It puts a Cache instance into React Router's typed request context, carries an application-derived identity frame, adopts background recovery work into the request lifecycle, and turns value-cache dependencies into scope-safe response tags.

:::caution[Server adapter, source preview]
This is a React Router v8 server integration. It is not a browser cache, a client data-fetching library, or a released package. The source adapter has unit and real Vite build coverage, but the npm package and production support policy do not exist yet.
:::

## Put Cache on the server

| Code location | Use Astilba Cache? | Why |
| --- | --- | --- |
| Server loader or action | Yes, through the middleware | The factory and invalidation call stay on the server. |
| Server Component or another server framework | Use the portable Cache API | The current framework adapter is specifically for React Router v8. |
| API route or backend service | Yes, through a runtime-owned Cache instance | Cache is ordinary server-side TypeScript. |
| Client Component or browser-only SPA | No | Browser request state and component lifecycles need a client data library. |

Build the Cache instance once for the server runtime. On Cloudflare, use <code>createWorkersCache()</code> as shown in [Cloudflare Workers](/docs/cache/cloudflare-workers/). That factory is safe at module scope: construction performs no I/O, and Coordinator handles plus the Bus connection are acquired lazily inside requests.

## Register the root middleware

React Router v8 enables middleware by default and removes the flag. A React Router v7 application must first enable <code>future.v8_middleware</code> as described in the [v7 upgrade guide](https://reactrouter.com/upgrading/v7#futurev8_middleware). This adapter targets v8; register <code>cacheMiddleware()</code> in the root route module as described by the current [middleware guide](https://reactrouter.com/how-to/middleware):

~~~tsx title="root.tsx (Cloudflare Workers)"
import { waitUntil } from "cloudflare:workers"
import type { MiddlewareFunction } from "react-router"

import { cacheMiddleware } from "@astilba/cache/react-router"

import { authMiddleware, authenticatedUserContext } from "./auth.server"
import { cache } from "./cache.server"

export const middleware: MiddlewareFunction[] = [
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

The <code>request</code> mapper is synchronous. Derive identity from a session or typed context that earlier trusted server middleware has already validated; do not treat an arbitrary client header as an authenticated principal.

If a request is intentionally anonymous, return <code>{}</code> or omit the mapper. With no visible principal and no explicit scope, the kernel resolves the call to the public storage class.

## Read in a loader

Use <code>cacheContext</code> to obtain the request's Cache instance. Pass <code>currentRequest()</code> into each read so the kernel can derive its privacy scope:

~~~tsx title="routes/product.tsx"
import type { Route } from "./+types/product"
import { t } from "@astilba/cache"
import {
  cacheContext,
  currentRequest,
} from "@astilba/cache/react-router"

export async function loader({ context, params }: Route.LoaderArgs) {
  const cache = context.get(cacheContext)
  const productId = params.productId

  const product = await cache.getOrSet({
    key: `product:${productId}`,
    tags: [t`product:${productId}`],
    request: currentRequest(),
    factory: ({ signal }) => loadProduct(productId, signal),
  })

  return { product }
}
~~~

<code>currentRequest()</code> returns the frame opened by the root middleware. Outside that frame it returns <code>undefined</code>. Calling <code>context.get(cacheContext)</code> without installing the middleware throws instead of silently constructing another cache; <code>cacheContext</code> follows React Router's [no-default <code>createContext()</code> behavior](https://reactrouter.com/api/utils/createContext).

After a mutation, change the source of truth first and invalidate through the same request Cache:

~~~ts title="routes/product-update.ts"
export async function action({ context, params, request }: Route.ActionArgs) {
  const cache = context.get(cacheContext)
  const productId = params.productId
  const input = await parseProductUpdate(request)

  await saveProduct(productId, input)
  await cache.delete({ tag: t`product:${productId}` })

  return { ok: true }
}
~~~

## Enable Workers compatibility

The adapter uses <code>AsyncLocalStorage</code> to make <code>currentRequest()</code> available throughout the request's async call tree. The root package also uses <code>node:crypto</code>, so Cloudflare Workers must enable:

~~~jsonc title="wrangler.jsonc (merge into your existing config)"
{
  // Minimum for nodejs_compat v2; use your project's current date.
  "compatibility_date": "2024-09-23",
  "compatibility_flags": ["nodejs_compat"]
}
~~~

<code>nodejs_compat</code> supplies both requirements when the Worker's compatibility date is 2024-09-23 or later, as required by Cloudflare's [Node.js compatibility guide](https://developers.cloudflare.com/workers/runtime-apis/nodejs/). The narrower <code>nodejs_als</code> flag alone does not provide <code>node:crypto</code> and cannot boot the package.

## Keep recovery work off the response path

At request start, the middleware asks the cache's replication poller whether a tick is due. It does not await that work before running loaders. Passing Cloudflare's <code>waitUntil</code> function allows an in-flight tick to continue after the response returns; Cloudflare documents that lifecycle in its [Context API guide](https://developers.cloudflare.com/workers/runtime-apis/context/#waituntil).

The adapter limits ticks to at most one per second per Cache instance. The poller's longer baseline, retry, and backoff schedules still decide whether a tick performs I/O. A failed tick is swallowed so it cannot fail the user's response; provide a <code>telemetry</code> sink if you need to observe <code>poll_tick_failed</code> events. <code>onSinkError</code> observes a telemetry sink that throws or rejects without allowing that failure to escape either.

<code>createWorkersCache()</code> also starts a best-effort recovery tick from value reads, so plain Workers applications recover without React Router. The middleware remains useful because it starts work at request entry and can adopt it with <code>waitUntil</code>. Neither path awaits recovery before continuing the user's request.

Without <code>waitUntil</code>, the tick is still started but is only best effort after the response lifecycle ends. Recovery does not become unsafe—the read path remains fail closed—but future requests may pay more live-check or refill work.

## Understand the response-cache posture

The middleware opens a request-scoped render collector around <code>next()</code>. Cache hits and successful fills automatically contribute their complete stored tag set and scope evidence. After the render, the middleware commits that collector against <code>L3_BUDGET_DEFAULT</code>—16 KB and 1,000 tag occurrences unless <code>l3Budget</code> overrides it.

The application still decides whether to opt into shared caching. The middleware never writes <code>public</code> or <code>s-maxage</code>:

| Render result | Header behavior |
| --- | --- |
| No managed Cache dependency | Preserve existing cache headers; add <code>Cache-Control: private</code> only when the application supplied no policy. |
| Eligible public dependencies | Preserve the application's cache policy and replace <code>Cache-Tag</code> with deduplicated user tags. If no policy exists, default to <code>private</code>. |
| Private, unreadable, late, or over-budget dependency | Force <code>Cache-Control: private</code>, remove <code>Cache-Tag</code>, and emit one <code>l3_ineligible</code> event. |

Reserved per-key and per-namespace tags never reach the response header. One tenant- or principal-scoped dependency makes the whole response private, even if its tag was marked droppable. Responses with immutable header guards are rebuilt so redirects and fetch-derived responses do not turn into middleware errors.

:::caution[Pass the request frame to reads]
Automatic render collection does not remove the need for <code>request: currentRequest()</code>. That option lets the kernel resolve principal and tenant scope. Omitting it from identity-bearing reads can make the value appear contextless and public.
:::

The adapter exports <code>L3_BUDGET_DEFAULT</code> and <code>L3_INELIGIBLE</code> alongside the poll constants. <code>CacheMiddlewareOptions</code> accepts <code>l3Budget</code>, <code>telemetry</code>, and <code>onSinkError</code> in addition to <code>cache</code>, identity mapping, and <code>waitUntil</code>.

The response-tag path does not purge a CDN. See [Cache HTTP responses](/docs/cache/response-caching/) for the complete safety and budget model, [Control cache sharing](/docs/cache/scopes-and-privacy/) for value storage, [Consistency and resilience](/docs/cache/consistency-and-resilience/) for recovery behavior, and [Implementation status](/docs/cache/api-status/) for current gaps.
