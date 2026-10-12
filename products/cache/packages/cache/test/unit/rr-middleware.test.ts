// Ordinary unit teeth (NOT sacred) — C4-a-2. The RR-v8 middleware shell against a REAL
// `RouterContextProvider` (react-router), no running Workers server. Each case bites one branch
// (rule 13): the cache-into-context provision (`context.set`), the ALS request frame (`run`), and
// the born-private header stamp (`headers.set(..., "private")`) — blinding any reds its case.

import assert from "node:assert/strict";

import { createCache } from "@astilba/cache";
import type { Cache, RequestContext } from "@astilba/cache";
import {
  cacheContext,
  cacheMiddleware,
  currentRequest,
} from "@astilba/cache/react-router";
import type { CacheMiddlewareArgs } from "@astilba/cache/react-router";
import { RouterContextProvider } from "react-router";
import { describe, expect, it } from "vitest";

/** A driverless build-smoke cache — the tests assert object identity + header/frame behavior, never
 * a fill, so no L2/registry is needed (and `createCache` never throws at construction). */
function makeCache(): Cache {
  return createCache({
    namespace: "rr-unit",
    clock: { now: () => 0 },
    rng: { next: () => 0 },
  });
}

/** The RR-v8 middleware argument object (`{ request, url, pattern, params, context }`). */
function makeArgs(context: RouterContextProvider): CacheMiddlewareArgs {
  return {
    context,
    params: {},
    pattern: "/",
    request: new Request("https://example.test/x"),
    url: new URL("https://example.test/x"),
  };
}

describe("cacheMiddleware — RouterContextProvider attach/provide/header (§7)", () => {
  it("provides the cache into RR context, retrievable via context.get inside next()", async () => {
    const cache = makeCache();
    const context = new RouterContextProvider();
    const middleware = cacheMiddleware({ cache });
    let seen: Cache | undefined;
    await middleware(makeArgs(context), async () => {
      seen = context.get(cacheContext);
      return new Response("ok");
    });
    // The exact instance passed in is the one a loader would read — not a copy.
    expect(seen).toBe(cache);
  });

  it("stamps the born-private Cache-Control: private default on the response", async () => {
    const cache = makeCache();
    const context = new RouterContextProvider();
    const middleware = cacheMiddleware({ cache });
    const response = await middleware(
      makeArgs(context),
      async () => new Response("ok")
    );
    // `assert.ok` narrows to Response without a conditional expect (and fails if it isn't one).
    assert.ok(response instanceof Response);
    // Exactly `private` — the conservative static default (no s-maxage, no Cache-Tag).
    expect(response.headers.get("cache-control")).toBe("private");
  });

  it("opens the ALS frame so currentRequest() reaches the derived principal inside next()", async () => {
    const cache = makeCache();
    const context = new RouterContextProvider();
    const middleware = cacheMiddleware({
      cache,
      request: (args) => ({
        path: new URL(args.request.url).pathname,
        userId: "u-42",
      }),
    });
    let seen: RequestContext | undefined;
    await middleware(makeArgs(context), async () => {
      seen = currentRequest();
      return new Response("ok");
    });
    expect(seen).toStrictEqual({ path: "/x", userId: "u-42" });
  });

  it("with no request derivation, the frame carries an empty RequestContext (the pub class)", async () => {
    const cache = makeCache();
    const context = new RouterContextProvider();
    const middleware = cacheMiddleware({ cache });
    let seen: RequestContext | undefined;
    await middleware(makeArgs(context), async () => {
      seen = currentRequest();
      return new Response("ok");
    });
    expect(seen).toStrictEqual({});
  });

  it("currentRequest() is undefined outside any middleware frame", () => {
    expect(currentRequest()).toBeUndefined();
  });
});
