// Ordinary teeth (NOT sacred) — the scope gate ON THE HERO PATH. A privacy gate's teeth belong
// where the users stand: `createWorkersCache` is the front door the API defines, and every other
// tooth in this repo drives raw `createCache` instead. The gap between those two surfaces is not
// hypothetical — it is where the gate died, silently, while every existing tooth stayed green.
//
// WHAT THE FACADE DOES, AND WHY IT CAN BREAK A GATE. The factory returns a WRAPPER around the
// instance `createCache` built (it hangs the freshness carrier on the read path). The kernel's
// dependency-recording seams are keyed BY INSTANCE, so a wrapper that the seams have never heard of
// records nothing: the app binds its collector to the wrapper, the kernel looks the collector up
// under the inner instance, the two never meet, and the render reports ZERO managed dependencies.
// Zero managed dependencies is the adapter's jurisdiction floor — it asserts nothing — so an
// app-set `public, s-maxage` header stands on a per-principal body. That is the leak the scope gate exists to
// close, re-opened by a facade.
//
// Each case bites ONE branch:
//   1. THE CONTROL — a tenant-scoped dependency read through the front door poisons the render:
//      eligible FALSE, reason `scope`, and the dependency is COUNTED. Drop the identity alias and
//      the emission comes back eligible with zero managed dependencies ⇒ red.
//   2. THE FORCE, at the front door — an app-set `public, s-maxage` cannot re-admit a poisoned
// response. the header-priority tooth already exists against raw `createCache`; this asserts
//      the same law over the REAL path a request takes — `createWorkersCache` → the middleware →
//      `decidePosture` → the response's own headers — because that is the composition that ships and
//      the seam it turns on exists nowhere else. Drop the alias and the app's public header stands.
//   3. THE POLLER SEAM — the second seam family through the same alias: the RR poll driver finds
//      the poller through the wrapper the app holds. Drop the alias and the driver goes dead.
//
// Lane: integration — the real `createWorkersCache` config names workerd's binding types, which a
// Node-side lane cannot resolve (the census's header carries the same constraint). Nothing here is
// timing-tolerant despite the lane: fake bindings, an injected principal, no waiting on anything.

import assert from "node:assert/strict";

import { t } from "@astilba/cache";
import { RouterContextProvider } from "react-router";
import { describe, expect, it } from "vitest";

import { createWorkersCache } from "../../src/adapters/cloudflare/workers-cache";
import type { WorkersCacheConfig } from "../../src/adapters/cloudflare/workers-cache";
import { bindRenderCollector, managedDependenciesOf } from "../../src/cache";
import type { Cache, RequestContext } from "../../src/cache";
import {
  cacheMiddleware,
  currentRequest,
} from "../../src/adapters/react-router/middleware";
import type { CacheMiddlewareArgs } from "../../src/adapters/react-router/middleware";
import { tickIfStale } from "../../src/adapters/react-router/poll-driver";

/** A KV namespace over a Map — the subject is the seam, not the store. */
function fakeKv(): KVNamespace {
  const map = new Map<string, { value: string; metadata: unknown }>();
  const kv = {
    delete: async (key: string): Promise<void> => {
      map.delete(key);
    },
    get: async (key: string): Promise<string | null> =>
      map.get(key)?.value ?? null,
    getWithMetadata: async (
      key: string
    ): Promise<{ value: string | null; metadata: unknown }> => {
      const hit = map.get(key);
      return { metadata: hit?.metadata ?? null, value: hit?.value ?? null };
    },
    list: async (): Promise<{
      keys: { name: string }[];
      list_complete: boolean;
    }> => ({ keys: [], list_complete: true }),
    put: async (
      key: string,
      value: string,
      opts?: { metadata?: unknown }
    ): Promise<void> => {
      map.set(key, { metadata: opts?.metadata ?? null, value });
    },
  };
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a structural stand-in for the KVNamespace surface this adapter reaches; the cases turn on the identity seam, not on KV's full API.
  return kv as unknown as KVNamespace;
}

/** A Coordinator namespace whose stub answers checks and refuses the bus dial (no bus in this rig —
 * the never-established path, which is not what these cases are about). */
function fakeCoordinator(): DurableObjectNamespace {
  const stub = {
    check: async (tags: readonly string[]): Promise<unknown[]> =>
      tags.map(() => ({
        hardEpoch: 0,
        known: true,
        softEpoch: 0,
        throughEpoch: 0,
      })),
    delete: async (): Promise<{ epoch: number }> => ({ epoch: 0 }),
    expire: async (): Promise<{ epoch: number }> => ({ epoch: 0 }),
    fetch: async (): Promise<Response> => {
      throw new Error("no bus (simulated)");
    },
    registerRetention: async (): Promise<void> => {
      // No horizon state needed here.
    },
  };
  const namespace = {
    get: (): unknown => stub,
    idFromName: (name: string): unknown => ({ toString: () => name }),
  };
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a structural stand-in for the DurableObjectNamespace surface the adapter reaches (idFromName + get).
  return namespace as unknown as DurableObjectNamespace;
}

/** The hero path exactly as the API defines it: name + kv + coordinator, nothing else. */
function heroCache(name: string): Cache {
  const config: WorkersCacheConfig = {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake stands in for the Coordinator-parameterized namespace; only idFromName/get are reached.
    coordinator:
      fakeCoordinator() as unknown as WorkersCacheConfig["coordinator"],
    kv: fakeKv(),
    name,
  };
  return createWorkersCache(config);
}

/** The RR-v8 middleware argument object (`{ request, url, pattern, params, context }`). */
function makeArgs(): CacheMiddlewareArgs {
  return {
    context: new RouterContextProvider(),
    params: {},
    pattern: "/",
    request: new Request("https://example.test/profile"),
    url: new URL("https://example.test/profile"),
  };
}

/**
 * The frame's `RequestContext`, narrowed. Inside a middleware frame `currentRequest()` is always
 * present; its type is `RequestContext | undefined` for the out-of-frame caller. The loader below
 * runs inside the frame by construction, so assert it rather than laundering the type.
 */
function frameRequest(): RequestContext {
  const request = currentRequest();
  assert.ok(request !== undefined, "loader ran outside the middleware frame");
  return request;
}

describe("E23 on the hero path — the scope gate survives the front door", () => {
  it("THE CONTROL: a tenant-scoped dependency read through createWorkersCache poisons the render", async () => {
    const cache = heroCache("hero-scope");
    const collector = cache.collect();
    // Bound to what the app HOLDS — the returned cache. That is the only object a hero-path user
    // ever has; there is no way for them to reach the instance behind it.
    bindRenderCollector(cache, () => collector);

    await cache.getOrSet({
      factory: async () => "p",
      key: "profile",
      scope: { tenant: "acme" },
    });

    const emission = collector.commitHeaders();

    // THE TOOTH. Drop the identity alias and this render reports `{cacheTags: [], eligible: true}`
    // with ZERO managed dependencies — a per-principal body declared shared-cacheable.
    expect(managedDependenciesOf(emission)).toBe(1);
    expect(emission.eligible).toBe(false);
    expect(emission.ineligibleReason).toBe("scope");
    expect(emission.cacheTags).toStrictEqual([]);
  });

  it("THE FORCE at the front door: a poisoned render outranks an app-set public header", async () => {
    // WHAT THIS CASE USED TO BE, recorded because the shape is the lesson: it built the same cache
    // as THE CONTROL, read the same entry, and asserted a subset of THE CONTROL's assertions. It
    // never set a `Cache-Control` header, never invoked the middleware, and never reached
    // `decidePosture` — so nothing it did could distinguish a working force from a missing one. A
    // tooth's NAME claims a jurisdiction; a tooth that never executes the path its name names is
    // worse than no tooth at all, because it answers the checklist while guarding nothing.
    //
    // So this now travels the whole hero path, exactly as a user's request does:
    //   createWorkersCache  →  cacheMiddleware  →  the loader's read  →  decidePosture  →  headers.
    const cache = heroCache("hero-force");
    // The app derives its principal — that is what makes the read below `usr:`-scoped.
    const middleware = cacheMiddleware({
      cache,
      request: () => ({ userId: "u-1" }),
    });

    const response = await middleware(makeArgs(), async () => {
      // The render consumes a per-principal dependency…
      await cache.getOrSet({
        factory: async () => "p",
        key: "profile",
        request: frameRequest(),
        tags: [t`user:1`],
      });
      // …and the route declares itself publicly cacheable anyway, with its own purge handle. This
      // is the liar the scope gate exists for: a `public, s-maxage` on a body that is one person's.
      return new Response("ok", {
        headers: {
          "Cache-Control": "public, s-maxage=60",
          "Cache-Tag": "route-x",
        },
      });
    });

    assert.ok(response instanceof Response);

    // THE TOOTH — one mutation, the alias-drop: sever the facade's identity alias and the kernel's
    // instance-keyed seams no longer resolve through the wrapper the app holds. The render then
    // reports ZERO managed dependencies, `decidePosture` takes its jurisdiction-floor branch, and
    // because the app DID set a cache header the floor is a no-edit — so all three of these red at
    // once and the app's `public, s-maxage=60` ships on a per-principal body. That is the leak,
    // stated as an assertion, standing where the users stand.
    //
    // The app's directive is OVERWRITTEN, not merged into and not deferred to: the whole point of
    // the branch is a route that asked to be shared-cached while its render read one principal's
    // content, and the app's own header is what has to lose.
    expect(response.headers.get("cache-control")).toBe("private");
    // The route's own purge handle is REMOVED, not merely left un-added — and the difference is the
    // branch. A CDN reads what is on the wire whether or not it elects to store the response, so a
    // tag riding out here would tell shared infrastructure how one person's cache is keyed and hand
    // it the means to purge that key. Being read is already the harm; storage is not the threshold.
    expect(response.headers.get("cache-tag")).toBeNull();
  });

  it("THE POLLER SEAM: the RR poll driver finds the poller through the cache the app holds", () => {
    const cache = heroCache("hero-poller");
    // The RR middleware drives the tick with the cache the APP passed it — the wrapper. Through the
    // alias that resolves to the instance the poller was registered against, so a tick is due and
    // fires. Drop the alias and this is `undefined`: the driver silently finds no poller and an RR
    // app on this factory loses its replication poll.
    expect(tickIfStale(cache, 0)).toBeDefined();
  });
});
