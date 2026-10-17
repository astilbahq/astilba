// Ordinary unit teeth (NOT sacred). A dependency declared during a fill JOINS
// the entry's stored tag set (exactly as if passed to `getOrSet`), so every later serving of that
// entry replays the full stored set — the CDN-cached copy carries the same purge handles as the
// fill, and `expire({tag})` reaches it. `setTags` authors that set; `dependsOn` adds membership;
// they union at settle. the ≤126 count ceiling is enforced over the settle-union.
//
// Each case bites ONE branch — the named mutation reds it:
//   1. round-trip        — persistence: envelope stores the dependsOn tag ⇒ a later HIT emits it,
//                          and `expire` reaches the entry. (Blind: `settleUserTags()` → declared.)
//   2. joiner-inherits   — a singleflight JOINER records the LEADER's stored set + scope, off the
//                          one fill outcome, never its own `options.tags`. (Blind: the fill record
//                          reads `options.tags` instead of `attempt.tags`.)
//   3. count ceiling     — route-independent: 127 via options ≡ 64 via options + 63 via dependsOn,
//                          same `InvalidTagError`. (Blind: drop the settle-union validation.)
//   4. boundary-exact    — 126 passes; 127 rejects. (Blind: the count comparison.)
//   5. setTags trio      — authored REPLACES declared · unions with dependsOn · ceiling fails fill.
//                          (Blind: `setTags` back to a no-op.)
//   6. post-settle       — dependsOn/setTags after the factory settled throw FactorySettledError.
//                          (Blind: drop the `isSettled()` guard.)
//   7. per-serving-path  — the stored set rides EVERY serve: stale/SWR (7a), graced (7b),
//                          neg-suppressed (7c). (Blind: `settleUserTags()` → declared reds all.)
//
// Lane: unit. Deterministic — an injected clock + fake store for the registry-absent cases; a
// SimClock `world()` for the ones that need staleness/grace (expire has no other author). No timing
// tolerance, no retry; the concurrency in tooth 2 is gated, not raced.

import assert from "node:assert/strict";
import { setImmediate as yieldTick } from "node:timers/promises";

import {
  createCache,
  FactorySettledError,
  HttpError,
  InvalidTagError,
  memory,
  t,
} from "@astilba/cache";
import type {
  Cache,
  FactoryCtx,
  RequestContext,
  TelemetryEvent,
} from "@astilba/cache";
import {
  cacheMiddleware,
  currentRequest,
  L3_INELIGIBLE,
} from "@astilba/cache/react-router";
import type { CacheMiddlewareArgs } from "@astilba/cache/react-router";
import { mkTag, world } from "@astilba/harness";
import { RouterContextProvider } from "react-router";
import { describe, expect, it } from "vitest";

// ── Harness (mirrors test/unit/rr-l3-scope.test.ts) ─────────────────────────────────────────────

/** A registry-absent cache over one durable tier — enough to fill, store, and hit. */
function makeCache(): Cache {
  return createCache({
    clock: { now: () => 0 },
    l2: memory(),
    namespace: "e24",
    rng: { next: () => 0 },
  });
}

/** A registry-bearing single-isolate cache (SimClock + seeded RNG). `expire` authors staleness,
 * which the stale/graced/neg serve paths are made of — a registry-absent cache reaches none. */
function registryCache(): Cache {
  return world({ isolates: 1 }).isolate(0).cache;
}

/** The frame's `RequestContext`, narrowed (loaders here run inside the middleware frame). */
function frameRequest(): RequestContext {
  const request = currentRequest();
  assert.ok(request !== undefined, "loader ran outside the middleware frame");
  return request;
}

function makeArgs(): CacheMiddlewareArgs {
  return {
    context: new RouterContextProvider(),
    params: {},
    pattern: "/",
    request: new Request("https://example.test/x"),
    url: new URL("https://example.test/x"),
  };
}

interface Rendered {
  readonly response: Response;
  readonly events: readonly TelemetryEvent[];
}

/** Run one request through the middleware. `userId` (when given) is the app's principal derivation
 * — the `usr:<h>` identity that makes a dependency private. */
async function render(
  cache: Cache,
  loader: () => Promise<Response>,
  userId?: string
): Promise<Rendered> {
  const events: TelemetryEvent[] = [];
  const middleware = cacheMiddleware({
    cache,
    telemetry: (event) => {
      events.push(event);
    },
    ...(userId === undefined ? {} : { request: () => ({ userId }) }),
  });
  const response = await middleware(makeArgs(), loader);
  assert.ok(response instanceof Response);
  return { events, response };
}

/** A loader that consumes one dependency and returns a response (no app cache headers, so an
 * eligible render's emitted `Cache-Tag` is exactly the adapter's assembled set). */
function loaderWith(consume: () => Promise<unknown>): () => Promise<Response> {
  return async () => {
    await consume();
    return new Response("ok");
  };
}

/** The events only (a registry-bearing cache also drives the poll tick). */
function l3Events(
  events: readonly TelemetryEvent[]
): readonly TelemetryEvent[] {
  return events.filter((event) => event.type === L3_INELIGIBLE);
}

/** A retriable origin failure (default classifier) — the graced serve's trigger. */
async function serverError(): Promise<never> {
  return await Promise.reject(
    new HttpError(new Response(null, { status: 503 }))
  );
}

/** A 404 — a `not_found` fact that, over a still-servable candidate, drives a neg-suppressed serve:
 *the neg-write is suppressed, the stale value is served. */
async function notFound(): Promise<never> {
  return await Promise.reject(
    new HttpError(new Response(null, { status: 404 }))
  );
}

/** N distinct raw user tags under one prefix — for the count-ceiling boundary. */
function tags(prefix: string, n: number): ReturnType<typeof mkTag>[] {
  return Array.from({ length: n }, (_unused, i) => mkTag(`${prefix}:${i}`));
}

/** The error a rejected promise settles with (`null` if it resolved). */
async function rejectionOf(promise: Promise<unknown>): Promise<unknown> {
  return await promise.then(
    () => null,
    (error: unknown) => error
  );
}

/** Drain microtasks + one macrotask cycle until `pred` holds (bounded) — deterministic in Node. */
async function flushUntil(pred: () => boolean, max = 2000): Promise<void> {
  for (let i = 0; i < max && !pred(); i++) {
    await yieldTick();
  }
}

// ── TOOTH 1: the round-trip ─────────────────────────────────────────────────────────────────────
describe("E24 — a dependency declared during a fill joins the stored set (§2.1, §2.3)", () => {
  // Mutation: `const userTags = accum.settleUserTags();` → `const userTags = declaredUserTags;` in
  // `fillAndStore` ⇒ the dependsOn tag never enters the envelope ⇒ the HIT emits nothing and the
  // purge cannot reach the entry ⇒ both assertions red.
  it("TOOTH 1 (round-trip): a later HIT emits the fill's dependsOn tag, and expire() on it reaches the entry", async () => {
    const cache = registryCache();
    const dep = t`dep:1`;
    let calls = 0;
    const call = async (): Promise<unknown> =>
      await cache.getOrSet({
        factory: async (ctx) => {
          calls++;
          ctx.dependsOn(dep);
          return await Promise.resolve("v");
        },
        key: "k",
        scope: "public",
      });

    // Request 1 FILLS it (its own emission rides the ctx path — not the point).
    await render(cache, loaderWith(call));

    // Request 2 HITS it — a fresh hit replays the STORED envelope's tags. The dependsOn tag is now
    // among them, so the CDN-cacheable copy carries it. (calls stays 1: no refill.)
    const hit = await render(cache, loaderWith(call));
    expect(calls).toBe(1);
    expect(hit.response.headers.get("cache-tag")).toBe("dep:1");

    // The purge handle works end to end: expire on that tag reaches the (hit-served) entry…
    await cache.expire({ tag: dep });
    const strong = await cache.getOrSet({
      consistency: "strong",
      factory: async (ctx) => {
        calls++;
        ctx.dependsOn(dep);
        return await Promise.resolve("v2");
      },
      key: "k",
      scope: "public",
    });
    // …so a strong read refills rather than serving the purged copy.
    expect(strong).toBe("v2");
    expect(calls).toBe(2);
  });
});

// ── TOOTH 2: the singleflight joiner inherits the leader's stored set + scope ────────────────────
describe("E24 — a singleflight joiner reports the leader's stored set (§2.1)", () => {
  /** Drive a leader + a joiner onto ONE fill, deterministically: the leader's factory parks on a
   * gate until both have registered/joined, so the second call cannot race ahead into a fresh hit
   * or its own fill. Returns the two renders and the factory call count (must be 1). */
  async function leaderJoiner(
    cache: Cache,
    scope: "public" | undefined,
    userId?: string
  ): Promise<{ joiner: Rendered; calls: number }> {
    const dep = t`dep:1`;
    // A deferred gate that holds the leader's fill open until the joiner has joined — a genuine
    // manual deferred, not incidental promise-wrapping. `resolve` is assigned synchronously inside
    // the executor, so `releaseGate` is set before any use (definite-assignment).
    let releaseGate!: () => void;
    // oxlint-disable-next-line promise/avoid-new -- a test-orchestration deferred; `Promise.withResolvers` is beyond this repo's TS lib target
    const gate = new Promise<void>((resolve) => {
      releaseGate = resolve;
    });
    let leaderStarted = false;
    let calls = 0;
    const factory = async (ctx: FactoryCtx<string>): Promise<string> => {
      calls++;
      leaderStarted = true;
      ctx.dependsOn(dep);
      await gate;
      return "v";
    };
    const call = async (): Promise<unknown> =>
      await cache.getOrSet({
        factory,
        key: "k",
        ...(scope === undefined ? {} : { scope }),
        ...(userId === undefined ? {} : { request: frameRequest() }),
      });

    // Start the leader; wait until its factory is running (registered in the singleflight map).
    const leaderRender = render(cache, loaderWith(call), userId);
    await flushUntil(() => leaderStarted);
    // Start the joiner; it misses every tier (nothing stored yet) and joins the in-flight leader.
    const joinerRender = render(cache, loaderWith(call), userId);
    await yieldTick();
    releaseGate();
    const [, joiner] = await Promise.all([leaderRender, joinerRender]);
    return { calls, joiner };
  }

  // Mutation: the fill record's `tags: attempt.tags ?? []` → `tags: sortedUserTags(options.tags)`
  // ⇒ the joiner records ITS OWN (empty) declared tags, not the leader's stored set ⇒ its Cache-Tag
  // comes out null ⇒ reds. (The LEADER is unaffected — its own ctx.dependsOn recorded the tag.)
  it("TOOTH 2 (joiner tags): a joiner emits the factory-declared tag it never itself declared", async () => {
    const { joiner, calls } = await leaderJoiner(makeCache(), "public");
    expect(calls).toBe(1); // coalesced: the joiner did NOT run the factory
    expect(joiner.response.headers.get("cache-tag")).toBe("dep:1");
  });

  // The SCOPE half of the same one-record channel: a private (usr:) fill's
  // joiner inherits the fill's scope evidence too, so it poisons. If the scope did not ride with the
  // tags off the one fill outcome, the joiner would read as unmanaged and the response would not be
  // stamped private-with-no-tag.
  it("TOOTH 2 (joiner scope): a joiner of a usr:-scoped fill inherits the private scope and poisons", async () => {
    const { joiner, calls } = await leaderJoiner(makeCache(), undefined, "u-1");
    expect(calls).toBe(1);
    expect(joiner.response.headers.get("cache-control")).toBe("private");
    expect(joiner.response.headers.get("cache-tag")).toBeNull();
    expect(l3Events(joiner.events)).toStrictEqual([
      { reason: "scope", type: L3_INELIGIBLE },
    ]);
  });
});

// ── TEETH 3 & 4: the count ceiling over the settle-union ────────────────────────────────────
describe("E24 — the ≤126 user-tag ceiling meets every source at one boundary (§2.3, MI13)", () => {
  // Mutation: drop the settle-union `validateUserTags(userTags)` in `fillAndStore` ⇒ the split-route
  // case stores 127 and RESOLVES ⇒ reds (the all-options case still throws at the boundary, so only
  // counting the union catches this).
  it("TOOTH 3 (route-independence): 127 via options ≡ 64 via options + 63 via dependsOn — same InvalidTagError", async () => {
    const cache = makeCache();
    const viaOptions = await rejectionOf(
      cache.getOrSet({
        factory: async () => await Promise.resolve("v"),
        key: "a",
        tags: tags("o", 127),
      })
    );
    const viaSplit = await rejectionOf(
      cache.getOrSet({
        factory: async (ctx) => {
          for (const tag of tags("d", 63)) {
            ctx.dependsOn(tag);
          }
          return await Promise.resolve("v");
        },
        key: "b",
        tags: tags("o", 64),
      })
    );
    expect(viaOptions).toBeInstanceOf(InvalidTagError);
    expect(viaSplit).toBeInstanceOf(InvalidTagError);
  });

  // Mutation: `tags.length > MAX_USER_TAGS` → `>=` (or drop the check) ⇒ 127 no longer rejects (or
  // 126 wrongly does) ⇒ reds.
  it("TOOTH 4 (boundary-exact): 126 user tags passes; 127 rejects", async () => {
    const cache = makeCache();
    await expect(
      cache.getOrSet({
        factory: async () => await Promise.resolve("v"),
        key: "ok",
        tags: tags("b", 126),
      })
    ).resolves.toBe("v");
    await expect(
      cache.getOrSet({
        factory: async () => await Promise.resolve("v"),
        key: "over",
        tags: tags("b", 127),
      })
    ).rejects.toBeInstanceOf(InvalidTagError);
  });
});

// ── TOOTH 5: setTags authors the declared set; dependsOn adds; they union at settle ──────────────
describe("E24 — setTags authors, dependsOn adds, union at settle (§2.1, §2.3)", () => {
  // Mutation: `setTags` back to a silent no-op ⇒ the stored set stays the DECLARED [decl:1] ⇒ the
  // emission is "decl:1", not "authored:1" ⇒ reds.
  it("TOOTH 5a (authored replaces declared): setTags([x]) over tags:[y] stores x, not y", async () => {
    const cache = makeCache();
    const { response } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async (ctx) => {
              ctx.setTags([t`authored:1`]);
              return await Promise.resolve("v");
            },
            key: "k",
            scope: "public",
            tags: [t`decl:1`],
          })
      )
    );
    expect(response.headers.get("cache-tag")).toBe("authored:1");
  });

  it("TOOTH 5b (union with dependsOn): the authored set and a dependsOn tag both ride, declared does not", async () => {
    const cache = makeCache();
    const { response } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async (ctx) => {
              ctx.setTags([t`authored:1`]);
              ctx.dependsOn(t`member:1`);
              return await Promise.resolve("v");
            },
            key: "k",
            scope: "public",
            tags: [t`decl:1`],
          })
      )
    );
    // The authored set and the membership tag both ride; the declared tag is gone. Order is the
    // collector's insertion-then-dedup, so compare the SET.
    const emitted = (response.headers.get("cache-tag") ?? "")
      .split(",")
      .toSorted();
    expect(emitted).toStrictEqual(["authored:1", "member:1"]);
  });

  it("TOOTH 5c (ceiling on the union fails the fill): 64 authored + 63 dependsOn rejects", async () => {
    const cache = makeCache();
    await expect(
      cache.getOrSet({
        factory: async (ctx) => {
          ctx.setTags(tags("a", 64));
          for (const tag of tags("d", 63)) {
            ctx.dependsOn(tag);
          }
          return await Promise.resolve("v");
        },
        key: "k",
      })
    ).rejects.toBeInstanceOf(InvalidTagError);
  });
});

// ── TOOTH 6: after the factory settles, further declarations throw ───────────────────────────────
describe("E24 — dependsOn/setTags after the factory settles throw FactorySettledError (§2.1)", () => {
  // Mutation: drop the `if (accum.isSettled()) throw new FactorySettledError(key)` guard from
  // dependsOn/setTags ⇒ the post-settle call is a silent no-op ⇒ neither throws ⇒ reds.
  it("TOOTH 6 (post-settle): a stashed ctx used after the fill throws on both dependsOn and setTags", async () => {
    const cache = makeCache();
    let captured: FactoryCtx<string> | undefined;
    await cache.getOrSet<string>({
      factory: async (ctx) => {
        captured = ctx;
        return await Promise.resolve("v");
      },
      key: "k",
    });
    assert.ok(captured !== undefined, "the factory never ran");
    const stashed = captured;
    expect(() => {
      stashed.dependsOn(t`late:1`);
    }).toThrow(FactorySettledError);
    expect(() => {
      stashed.setTags([t`late:2`]);
    }).toThrow(FactorySettledError);
  });
});

// ── TOOTH 7: the stored set rides EVERY serving path ─────────────────────────────────────────────
// A fill's dependsOn tag must reach the response no matter WHICH serve path replays the stored
// entry. Fresh-hit is tooth 1; here are the three that need staleness. The graced (7b) and
// neg-suppressed (7c) serves share ONE recordServedHit site — two fixtures guard the structure so a
// future branch-split cannot orphan neg-suppressed behind a still-green grace tooth. One shared
// blinding mutation (`settleUserTags()` → declared, so the candidate never stored the tag) reds all
// three: the served copy would carry no purge handle.
describe("E24 — a served value reports the stored set on every serve path (§2.3)", () => {
  it("TOOTH 7a (stale/SWR serve): a soft-purged entry served stale still emits its dependsOn tag", async () => {
    const cache = registryCache();
    const dep = t`dep:1`;
    const read = async (): Promise<unknown> =>
      await cache.getOrSetEntry({
        factory: async (ctx) => {
          ctx.dependsOn(dep);
          return await Promise.resolve("p");
        },
        grace: "1h",
        key: "k",
        scope: "public",
      });

    await render(cache, loaderWith(read)); // fill
    await cache.expire({ tag: dep }); // soft-purge ⇒ stale but SWR-servable

    let served: unknown;
    const { response } = await render(
      cache,
      loaderWith(async () => {
        served = await read();
      })
    );
    expect(served).toMatchObject({ stale: true, value: "p" });
    expect(response.headers.get("cache-tag")).toBe("dep:1");
  });

  it("TOOTH 7b (graced serve): an error-path (stale-if-error) serve emits the candidate's dependsOn tag", async () => {
    const cache = registryCache();
    const dep = t`dep:1`;
    await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSetEntry({
            consistency: "strong",
            factory: async (ctx) => {
              ctx.dependsOn(dep);
              return await Promise.resolve("p");
            },
            grace: "1h",
            key: "k",
            scope: "public",
          })
      )
    );
    await cache.expire({ tag: dep });

    let served: unknown;
    const { response } = await render(
      cache,
      loaderWith(async () => {
        // `strong` routes the stale entry to the foreground fill; the failing factory meets it as
        // the grace CANDIDATE — the error-path serve under test.
        served = await cache.getOrSetEntry({
          consistency: "strong",
          factory: serverError,
          grace: "1h",
          key: "k",
          scope: "public",
        });
      })
    );
    expect(served).toMatchObject({ servedOnError: true, stale: true });
    expect(response.headers.get("cache-tag")).toBe("dep:1");
  });

  it("TOOTH 7c (neg-suppressed serve): a 404 over a servable candidate serves it and emits the dependsOn tag", async () => {
    const cache = registryCache();
    const dep = t`dep:1`;
    await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSetEntry({
            consistency: "strong",
            factory: async (ctx) => {
              ctx.dependsOn(dep);
              return await Promise.resolve("p");
            },
            grace: "1h",
            key: "k",
            notFoundTtl: "1h",
            scope: "public",
          })
      )
    );
    await cache.expire({ tag: dep });

    let served: unknown;
    const { response } = await render(
      cache,
      loaderWith(async () => {
        // A 404 with a still-servable candidate: the neg-write is suppressed and the stale val
        // is served — NOT an error-path serve, so no `servedOnError`.
        served = await cache.getOrSetEntry({
          consistency: "strong",
          factory: notFound,
          grace: "1h",
          key: "k",
          notFoundTtl: "1h",
          scope: "public",
        });
      })
    );
    expect(served).toMatchObject({ stale: true, value: "p" });
    expect(response.headers.get("cache-tag")).toBe("dep:1");
  });
});
