// Ordinary unit teeth (NOT sacred) — C4-c. The §7.2/E23 scope gate, end to end: a render's
// consumed dependencies reach the collector through the kernel's WeakMap seam + the middleware's
// AsyncLocalStorage frame, and the middleware turns the resulting emission into headers.
//
// Every case here bites ONE branch (rule 13) — blinding it reds the named case:
//   1. the FORCE          — an app-set public header cannot re-admit a poisoned response
//   2. the scope gate     — blinded (always eligible) ⇒ a private tag ships
//   3. the ANY predicate  — one poisoned dep poisons a mixed render (not ALL-non-public)
//   4. the override       — an explicit s-maxage is overridden, not merged
//   5. the jurisdiction   — zero managed dependencies ⇒ the adapter asserts only a safe DEFAULT
//                           (this is the tooth that replaces C4-a-2's unconditional stamp)
//   6. the strip          — a Cache-Tag the adapter did not assemble is DELETED off the response,
//                           not just left un-set (a downstream cache parses what is on the wire)
//   7. the raw-scope read — the gate reads the STORED scope, never the E17-reconstructed one
//   8. severability       — l3:false suppresses the TAG and still contributes the SCOPE
//   9. the STALE/SWR serve — a stale-served private entry poisons (a SERVE is a consumption)
//  10. the GRACED serve   — an error-path (stale-if-error) serve of a private entry poisons
//  11. the separator bytes — the budget measures the header as JOINED, not as bare tags
//  12. the eligible default-fill — an eligible render that set no cache header still gets private
//
// Lane: unit. Deterministic by construction — an injected clock, a fake store, and a fixture that
// makes the unreadable-scope case reproducible without any platform. Teeth 9/10 need a registry
// (staleness has no other author), so they run on the harness `world()` — a SimClock world, still
// fully deterministic. No timing tolerance, no retry.

import assert from "node:assert/strict";
import { setImmediate } from "node:timers/promises";

import {
  createCache,
  HttpError,
  memory,
  NotImplementedError,
  t,
} from "@astilba/cache";
import type {
  Cache,
  RequestContext,
  Store,
  StoreMetadata,
  TelemetryEvent,
} from "@astilba/cache";
import {
  cacheMiddleware,
  currentRequest,
  L3_INELIGIBLE,
} from "@astilba/cache/react-router";
import type { CacheMiddlewareArgs } from "@astilba/cache/react-router";
import { world } from "@astilba/harness";
import { RouterContextProvider } from "react-router";
import { describe, expect, it } from "vitest";

// The in-package seam the middleware itself reads the consumed-dependency count off (§7, E23). It
// is NOT public API (not re-exported from the barrel), so the tooth reaches it exactly the way its
// one production consumer does — by module path, from inside the package.
import { managedDependenciesOf } from "../../src/cache";

/** A registry-absent cache over a single durable tier — enough to fill, store, and hit. The scope
 * gate reads what the store returns, so the store is the only fixture surface the teeth need. */
function makeCache(l2: Store = memory()): Cache {
  return createCache({
    clock: { now: () => 0 },
    l2,
    namespace: "l3",
    rng: { next: () => 0 },
  });
}

/**
 * A `Store` whose reads return metadata with the `scope` field REMOVED — a stored entry whose scope
 * is unreadable on the shared surface (an entry written by an older kernel, a lossy migration, a
 * driver that drops unknown metadata). Writes pass through untouched, so the entry is otherwise
 * completely ordinary: only the evidence the eligibility gate depends on is missing.
 */
function scopeStrippingStore(inner: Store): Store {
  return {
    delete: async (key) => {
      await inner.delete(key);
    },
    get: async (key) => {
      const stored = await inner.get(key);
      if (stored?.metadata === undefined) {
        return stored;
      }
      const stripped: StoreMetadata = Object.fromEntries(
        Object.entries(stored.metadata).filter(([field]) => field !== "scope")
      );
      return { metadata: stripped, value: stored.value };
    },
    set: async (key, value, options) => {
      await inner.set(key, value, options);
    },
  };
}

/**
 * The frame's `RequestContext`, narrowed. Inside a middleware frame `currentRequest()` is always
 * present (the middleware installs the empty context when no derivation is configured), but its
 * type is `RequestContext | undefined` for the OUT-of-frame caller — and `exactOptionalPropertyTypes`
 * rightly refuses to let an explicit `undefined` be passed for an optional `request`. These loaders
 * run inside the frame by construction, so assert it rather than laundering the type.
 */
function frameRequest(): RequestContext {
  const request = currentRequest();
  assert.ok(request !== undefined, "loader ran outside the middleware frame");
  return request;
}

/** The RR-v8 middleware argument object (`{ request, url, pattern, params, context }`). */
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
 * — the identity the kernel hashes into `usr:<h>` (§6, E17), i.e. what makes a dependency private. */
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
  // `assert.ok` narrows to Response without a conditional expect (and fails if it isn't one).
  assert.ok(response instanceof Response);
  return { events, response };
}

/** The §7.2/E23 events only. A registry-bearing cache also drives the C4-b poll tick, whose own
 * `poll_tick_failed` counter shares this sink — it is a different subject and is not under test
 * here, so the L3 assertions read the L3 events. */
function l3Events(
  events: readonly TelemetryEvent[]
): readonly TelemetryEvent[] {
  return events.filter((event) => event.type === L3_INELIGIBLE);
}

/** A registry-bearing single-isolate cache. `expire` is what authors staleness, and staleness is
 * what the two SERVE paths below (stale/SWR, graced) are made of — a registry-absent cache can
 * reach neither. Deterministic (SimClock + seeded RNG). */
function registryCache(): Cache {
  return world({ isolates: 1 }).isolate(0).cache;
}

/** A retriable origin failure (§5.4, E2's default HTTP classifier) — the graced serve's trigger. */
async function serverError(): Promise<never> {
  return await Promise.reject(
    new HttpError(new Response(null, { status: 503 }))
  );
}

/** A loader that consumes one dependency and returns a response carrying the app's own headers. */
function loaderWith(
  consume: () => Promise<unknown>,
  headers: Record<string, string> = {}
): () => Promise<Response> {
  return async () => {
    await consume();
    return new Response("ok", { headers });
  };
}

describe("E23 — scope poisons shared-cacheability (§7.2)", () => {
  // ── CONTROL: the gate is not just "always private" ──────────────────────────────────────────
  it("CONTROL: a public-only render is eligible — the adapter emits its assembled Cache-Tag and preserves the app's own Cache-Control", async () => {
    const cache = makeCache();
    const { response, events } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "b",
            key: "book",
            scope: "public",
            tags: [t`book:1`],
          }),
        { "Cache-Control": "public, s-maxage=60" }
      )
    );
    // The adapter emits the purge handle it assembled…
    expect(response.headers.get("cache-tag")).toBe("book:1");
    // …and never fabricates shared caching of its own: the app's directive stands as written.
    expect(response.headers.get("cache-control")).toBe("public, s-maxage=60");
    expect(events).toStrictEqual([]);
  });

  // ── TOOTH 1: the FORCE ──────────────────────────────────────────────────────────────────────
  // Mutation: drop the `cacheControl: PRIVATE` from the ineligible posture in `decidePosture` ⇒
  // the app's `public, s-maxage=60` survives ⇒ reds.
  it("TOOTH 1 (force): a liar route declaring public+Cache-Tag while the render read a usr:-scoped entry comes out private with no Cache-Tag", async () => {
    const cache = makeCache();
    const { response } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "p",
            key: "profile",
            request: frameRequest(),
            tags: [t`user:1`],
          }),
        { "Cache-Control": "public, s-maxage=60", "Cache-Tag": "route-x" }
      ),
      "u-1"
    );
    expect(response.headers.get("cache-control")).toBe("private");
    expect(response.headers.get("cache-tag")).toBeNull();
  });

  // ── TOOTH 2: the eligibility check itself ───────────────────────────────────────────────────
  // Mutation: blind the scope gate in `collect().commitHeaders` (drop the `if (poisoned)` branch,
  // i.e. always fall through to eligible) ⇒ the private entry's tag is emitted and no telemetry
  // event fires ⇒ reds.
  it("TOOTH 2 (gate): a usr:-scoped dependency alone makes the response ineligible — no Cache-Tag, one scope event", async () => {
    const cache = makeCache();
    const { response, events } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "p",
            key: "profile",
            request: frameRequest(),
            tags: [t`user:1`],
          })
      ),
      "u-1"
    );
    expect(response.headers.get("cache-control")).toBe("private");
    expect(response.headers.get("cache-tag")).toBeNull();
    // Exactly ONE event, naming the reason class, carrying no tag names.
    expect(events).toStrictEqual([{ reason: "scope", type: L3_INELIGIBLE }]);
  });

  // ── TOOTH 3: ANY, not ALL ───────────────────────────────────────────────────────────────────
  // Mutation: change the poison predicate from ANY-non-public to ALL-non-public (latch a
  // `publicSeen` flag on a `pub` dependency and require `poisoned && !publicSeen`) ⇒ the public
  // book dependency re-admits the render ⇒ the tags ship ⇒ reds.
  it("TOOTH 3 (any-not-all): a mixed render — one pub dependency, one usr: dependency — degrades to private with no tags", async () => {
    const cache = makeCache();
    const { response, events } = await render(
      cache,
      loaderWith(async () => {
        await cache.getOrSet({
          factory: async () => "b",
          key: "book",
          scope: "public",
          tags: [t`book:1`],
        });
        await cache.getOrSet({
          factory: async () => "p",
          key: "profile",
          request: frameRequest(),
          tags: [t`user:1`],
        });
      }),
      "u-1"
    );
    expect(response.headers.get("cache-control")).toBe("private");
    // Neither tag ships — not even the public one. The RESPONSE is the poisoned object.
    expect(response.headers.get("cache-tag")).toBeNull();
    expect(events).toStrictEqual([{ reason: "scope", type: L3_INELIGIBLE }]);
  });

  // ── TOOTH (finding #3): the l3_ineligible emit is ROUTED through safeEmit ───────────────────
  // The l3 emit is on the SYNCHRONOUS response path — a throwing app sink NOT routed through
  // safeEmit would reject the middleware's own promise: a 500 on a route that merely DEMOTED to
  // private. This tooth certifies ROUTING (this path goes through the guard); safe-emit.test.ts
  // certifies both guard MODES (an oracle certifies only the dimensions it compares). Mutation:
  // revert the middleware's l3 emit from `safeEmit(...)` to a raw `telemetry?.({...})` call ⇒ the
  // throwing sink rejects the middleware ⇒ the poisoned request 500s ⇒ reds.
  it("finding #3 (routing): a throwing telemetry sink cannot 500 the demoted response", async () => {
    const cache = makeCache();
    const middleware = cacheMiddleware({
      cache,
      request: () => ({ userId: "u-1" }),
      telemetry: (): void => {
        throw new Error("sink threw on the synchronous response path");
      },
    });
    const escaped: unknown[] = [];
    const onUnhandled = (reason: unknown): void => {
      escaped.push(reason);
    };
    process.on("unhandledRejection", onUnhandled);
    let response: unknown;
    try {
      // A poisoned render (a usr:-scoped dependency) ⇒ the l3_ineligible emit fires ⇒ the throwing
      // sink is reached on the response path.
      response = await middleware(
        makeArgs(),
        loaderWith(
          async () =>
            await cache.getOrSet({
              factory: async () => "p",
              key: "profile",
              request: frameRequest(),
              tags: [t`user:1`],
            })
        )
      );
      await setImmediate(); // one macrotask turn: Node's unhandled-rejection sweep runs here
    } finally {
      process.off("unhandledRejection", onUnhandled);
    }
    // safeEmit swallowed the throw: the demoted response survived, still private, and nothing floated.
    assert.ok(response instanceof Response);
    expect(response.headers.get("cache-control")).toBe("private");
    expect(escaped).toStrictEqual([]);
  });

  // ── TOOTH 4: the override ───────────────────────────────────────────────────────────────────
  // Same force as tooth 1, isolated to the Cache-Control side: no foreign tag in play, so the ONLY
  // thing under test is that an app-set shared-cache directive is REPLACED, not merged or respected.
  it("TOOTH 4 (override): an app-set public/s-maxage header cannot re-admit a poisoned response", async () => {
    const cache = makeCache();
    const { response } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "p",
            key: "profile",
            request: frameRequest(),
            tags: [t`user:1`],
          }),
        { "Cache-Control": "public, max-age=0, s-maxage=600" }
      ),
      "u-1"
    );
    expect(response.headers.get("cache-control")).toBe("private");
  });

  // ── TOOTH 5: the jurisdiction floor ─────────────────────────────────────────────────────────
  // THIS IS THE TOOTH THAT REPLACES C4-a-2's UNCONDITIONAL `private` STAMP. a-2 stamped `private`
  // on every response, always; C4-c narrows that to a DEFAULT-FILL over renders the library knows
  // nothing about. Mutation: restore the a-2 stamp (make `decidePosture` return
  // `{ cacheControl: PRIVATE }` unconditionally) ⇒ the app's own directive is clobbered ⇒ reds.
  it("TOOTH 5 (jurisdiction): a render that consumed NO managed dependency keeps the app's own Cache-Control and Cache-Tag", async () => {
    const cache = makeCache();
    const { response, events } = await render(
      cache,
      async () =>
        // No cache call at all — the adapter has observed nothing, so it asserts nothing.
        new Response("ok", {
          headers: {
            "Cache-Control": "public, s-maxage=60",
            "Cache-Tag": "app-owned",
          },
        })
    );
    expect(response.headers.get("cache-control")).toBe("public, s-maxage=60");
    expect(response.headers.get("cache-tag")).toBe("app-owned");
    expect(events).toStrictEqual([]);
  });

  it("TOOTH 5b (safe default): a render that consumed NO managed dependency and set NO cache header still gets private", async () => {
    const cache = makeCache();
    const { response } = await render(cache, async () => new Response("ok"));
    // The floor is a DEFAULT, not an absence of policy: silence still resolves to `private`.
    expect(response.headers.get("cache-control")).toBe("private");
  });

  // ── TOOTH 5c: the ELIGIBLE path's safe default ───────────────────────────────────────────────
  // The `private` fill-in is not only the jurisdiction floor's (tooth 5b) — it applies on the
  // ELIGIBLE path too. The adapter emits the purge tags, and still never fabricates shared caching:
  // opting in is the application's call, so a render that said nothing gets `private`, tags and all.
  // Mutation: drop the `...safeDefault` spread from `decidePosture`'s eligible return ⇒ no
  // `Cache-Control` is written at all ⇒ reds. (The CONTROL case above cannot catch this: its app
  // sets its own header, which is precisely the branch the default-fill does NOT take.)
  it("TOOTH 5c (eligible default): an eligible public-only render that set NO cache header gets private — with its Cache-Tag", async () => {
    const cache = makeCache();
    const { response, events } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "b",
            key: "book",
            scope: "public",
            tags: [t`book:1`],
          })
        // …and NO app headers at all.
      )
    );
    // Eligible: the assembled purge handle ships.
    expect(response.headers.get("cache-tag")).toBe("book:1");
    // And the app opted into nothing, so the adapter fills in the safe default rather than silence.
    expect(response.headers.get("cache-control")).toBe("private");
    expect(l3Events(events)).toStrictEqual([]);
  });

  // ── TOOTH 6: the strip ──────────────────────────────────────────────────────────────────────
  // Mutation: drop the `cacheTag: null` (the DELETE) from the ineligible posture in `applyHeaders`
  // ⇒ the app's own tag survives on a poisoned response ⇒ reds. Declining to ADD a tag is not the
  // same as removing one: what a downstream cache reads is whatever is on the wire.
  it("TOOTH 6 (strip): a Cache-Tag the adapter did not assemble is REMOVED from a poisoned response", async () => {
    const cache = makeCache();
    const { response } = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "p",
            key: "profile",
            request: frameRequest(),
            tags: [t`user:1`],
          }),
        { "Cache-Tag": "app-owned,another" }
      ),
      "u-1"
    );
    expect(response.headers.get("cache-tag")).toBeNull();
  });

  // ── TOOTH 7: the RAW stored scope, never the reconstructed one ───────────────────────────────
  // The plumbing's reason for existing. `envelopeFromStore` substitutes `pub` for a missing stored
  // scope (E17), which leaves `envelope.scope` unable to tell a stored-public entry from one that
  // stored no scope at all — and telling those apart is the whole job of the gate. Mutation: make
  // the reconstructed `envelope.scope` instead of the raw stored field (in `readTier`, set
  // `rawScope: envelope.scope` / `scopeUnreadable: false`) ⇒ the entry reads as `pub` ⇒ eligible ⇒
  // reds.
  it("TOOTH 7 (raw scope): a stored entry whose scope is absent from the store's metadata poisons the response as scope_unreadable", async () => {
    const cache = makeCache(scopeStrippingStore(memory()));
    // Request 1 FILLS the entry (public, durable) — a perfectly ordinary write.
    const first = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "b",
            key: "book",
            scope: "public",
            tags: [t`book:1`],
          })
      )
    );
    // The fill's scope is COMPUTED, so the fill itself is clean and eligible…
    expect(first.response.headers.get("cache-tag")).toBe("book:1");

    // Request 2 HITS it — and the store returns metadata with no `scope` field at all.
    const second = await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSet({
            factory: async () => "b",
            key: "book",
            scope: "public",
            tags: [t`book:1`],
          })
      )
    );
    // …but a scope that cannot be READ is not a scope that is public. Fail closed.
    expect(second.response.headers.get("cache-control")).toBe("private");
    expect(second.response.headers.get("cache-tag")).toBeNull();
    expect(second.events).toStrictEqual([
      { reason: "scope_unreadable", type: L3_INELIGIBLE },
    ]);
  });

  /* References: E25, E24. */

  it("TOOTH 8a (l3:false refusal, plain form): dependsOn(tag, {l3:false}) inside a getOrSet factory refuses, naming v0.5", async () => {
    const cache = makeCache();
    const err: unknown = await cache
      .getOrSet({
        factory: async (ctx) => {
          ctx.dependsOn(t`user:1|sidebar`, { l3: false });
          return "p";
        },
        key: "profile",
      })
      .then(
        () => {},
        (error: unknown) => error
      );
    // `assert.ok` narrows to NotImplementedError (no cast) — its message names the v0.5 target.
    assert.ok(err instanceof NotImplementedError);
    expect(err.message).toContain("v0.5");
    expect(err.message).toContain("l3");
  });

  it("TOOTH 8b (l3:false refusal, entry form): dependsOn(tag, {l3:false}) inside a getOrSetEntry factory refuses, naming v0.5", async () => {
    const cache = makeCache();
    // The refusal fires the instant `ctx.dependsOn` sees `l3:false` — BEFORE `ctx.skip()` is
    // reached — so the entry form rejects with the typed refusal, never a skipped miss.
    const err: unknown = await cache
      .getOrSetEntry({
        factory: (ctx) => {
          ctx.dependsOn(t`user:1|sidebar`, { l3: false });
          return ctx.skip();
        },
        key: "sidebar",
      })
      .then(
        () => {},
        (error: unknown) => error
      );
    assert.ok(err instanceof NotImplementedError);
    expect(err.message).toContain("v0.5");
    expect(err.message).toContain("l3");
  });

  // The severability branch's new test home (§7, E23). `collect().dependsOn` stores nothing, so
  // `l3:false` stays LIVE there and the suppression is directly observable on an ELIGIBLE public
  // render — no `usr:` scope in play to mask it. Mutation: drop `if (l3 === false) { return; }` from
  // `collect()`'s `record` ⇒ the suppressed tag emits ⇒ the emitted Cache-Tag carries `dropped:1` ⇒
  // reds.
  it("TOOTH 8c (collector severability): l3:false suppresses its tag from an eligible public render's Cache-Tag; a default-l3 tag emits", () => {
    const collector = makeCache().collect();
    collector.dependsOn(t`kept:1`); // default l3 — emitted
    collector.dependsOn(t`dropped:1`, { l3: false }); // severable — suppressed
    const emission = collector.commitHeaders();
    // A bare public collector carries no scope claim, so the render is ELIGIBLE — the suppression
    // is visible in what the header would carry, not masked behind a poisoned (empty) emission.
    expect(emission.eligible).toBeTruthy();
    expect(emission.cacheTags).toStrictEqual(["kept:1"]);
  });
});

// ── The seam's fail-closed property (§7, E23) ───────────────────────────────────────────────────
// The kernel records ONLY through the accessor the middleware binds. A cache used without the
// middleware therefore collects nothing and emits nothing — there is no path by which a header
// could be stamped on a response the adapter never saw. This is the reason `createWorkersCache`'s
// quickstart (E22) needs no wiring line: the safe state is the unwired one.
// ── The two SERVE paths that are not a fresh hit (§7.2, E23) ────────────────────────────────────
// A fresh L1/L2 hit is not the only way a stored entry reaches a response. Two more paths SERVE a
// stored value, and each is a consumption exactly like a hit:
//
//   • the STALE/SWR serve — a soft-purged entry is served `stale:true` while a refresh runs for
//     next time (the served value is ALWAYS the stale one);
//   • the GRACED serve — the factory failed retriably and a still-servable stale copy is served
//     instead (stale-if-error).
//
// Neither is reachable on a registry-ABSENT cache: with no invalidation authority every read
// validates fresh, so `stale` never arises and no candidate is ever carried. That is why the teeth
// above — all of which build a registry-absent cache — cannot reach these branches, and why these
// two run on a registry-bearing `world()`. A serve that records no scope evidence is precisely the
// leak E23 exists to close: the response would go out shared-cacheable with a per-principal body.
describe("E23 — a SERVE is a consumption on every serve path (§7.2)", () => {
  // ── TOOTH 9: the STALE/SWR serve ────────────────────────────────────────────────────────────
  // Mutation: blind `recordServedHit(ambient, read.hit)` on the stale/SWR branch of `runGetOrSet`
  // ⇒ the stale-served usr: entry contributes no scope evidence ⇒ the render reads as unmanaged ⇒
  // the jurisdiction floor leaves the app's `public, s-maxage=60` + `Cache-Tag` intact ⇒ reds.
  it("TOOTH 9 (stale serve): a usr:-scoped entry served STALE (SWR) poisons the response", async () => {
    const cache = registryCache();
    const tag = t`user:1`;
    const read = async (): Promise<unknown> =>
      await cache.getOrSetEntry({
        factory: async () => await Promise.resolve("p"),
        grace: "1h",
        key: "profile",
        request: frameRequest(),
        tags: [tag],
      });
    const appHeaders = {
      "Cache-Control": "public, s-maxage=60",
      "Cache-Tag": "route-x",
    };

    // Request 1 fills it. Request 1's own poisoning comes from the FILL recording (tooth 2's
    // branch), so it proves nothing about the serve path — it only lands the entry.
    await render(cache, loaderWith(read, appHeaders), "u-1");
    // Soft-purge the tag: the entry is now STALE — still servable, and SWR serves it.
    await cache.expire({ tag });

    let served: unknown;
    const { response, events } = await render(
      cache,
      loaderWith(async () => {
        served = await read();
        return served;
      }, appHeaders),
      "u-1"
    );
    // Prove the path: this render really did take the stale/SWR serve, not a fresh hit or a refill.
    expect(served).toMatchObject({ stale: true, value: "p" });
    // A stale-served private body is still a private body.
    expect(response.headers.get("cache-control")).toBe("private");
    expect(response.headers.get("cache-tag")).toBeNull();
    expect(l3Events(events)).toStrictEqual([
      { reason: "scope", type: L3_INELIGIBLE },
    ]);
  });

  // ── TOOTH 10: the GRACED (error-path) serve ─────────────────────────────────────────────────
  // Mutation: blind `recordServedHit(ambient, staleCandidate)` on the graced/neg-suppressed branch
  // of `runGetOrSet` ⇒ same shape as tooth 9: no evidence, unmanaged render, the app's public
  // header and tag survive on a per-principal body ⇒ reds.
  //
  // `consistency: "strong"` is what routes the stale entry to the FOREGROUND fill rather than to
  // the SWR serve above (a strong read must not serve an observed-soft-purged value), so the failing
  // factory meets it as the error-path grace CANDIDATE — which is the branch under test.
  it("TOOTH 10 (graced serve): a usr:-scoped entry served on the ERROR path (stale-if-error) poisons the response", async () => {
    const cache = registryCache();
    const tag = t`user:1`;
    const appHeaders = {
      "Cache-Control": "public, s-maxage=60",
      "Cache-Tag": "route-x",
    };

    await render(
      cache,
      loaderWith(
        async () =>
          await cache.getOrSetEntry({
            consistency: "strong",
            factory: async () => await Promise.resolve("p"),
            grace: "1h",
            key: "profile",
            request: frameRequest(),
            tags: [tag],
          }),
        appHeaders
      ),
      "u-1"
    );
    await cache.expire({ tag });

    let served: unknown;
    const { response, events } = await render(
      cache,
      loaderWith(async () => {
        served = await cache.getOrSetEntry({
          consistency: "strong",
          factory: serverError,
          grace: "1h",
          key: "profile",
          request: frameRequest(),
          tags: [tag],
        });
        return served;
      }, appHeaders),
      "u-1"
    );
    // Prove the path: `servedOnError` is the error-path grace serve's own signature (§5.4).
    expect(served).toMatchObject({
      servedOnError: true,
      stale: true,
      value: "p",
    });
    // A response does not become shared-cacheable because the origin was down.
    expect(response.headers.get("cache-control")).toBe("private");
    expect(response.headers.get("cache-tag")).toBeNull();
    expect(l3Events(events)).toStrictEqual([
      { reason: "scope", type: L3_INELIGIBLE },
    ]);
  });
});

// ── The IMMUTABLE-response rescue (§7.2, E23) ───────────────────────────────────────────────────
// `Response.redirect(...)`, a `fetch()` passthrough, and a `caches.match(...)` result all carry an
// IMMUTABLE header guard: every `set`/`delete` on their headers throws `TypeError`. A middleware
// that stamps headers unconditionally rejects its own promise on such a response — i.e. a route
// that merely redirects becomes a 500. The posture must land anyway, on a rebuilt copy.
//
// BOTH SHAPES ARE TOOTHED, and the second is not a duplicate of the first. A redirect carries no
// BODY, so a rescue that rebuilt the response while dropping its body would sail past every
// redirect case here. A proxied `fetch()` response — the shape a route that fronts an upstream
// returns — carries the same immutable guard AND a body, so it witnesses what the redirect cannot.
describe("E23 — the posture lands on a response whose headers are immutable", () => {
  const REDIRECT_TO = "https://example.test/login";

  // Mutation: drop the try/catch + rebuild in `applyHeaders` (write straight to
  // `response.headers`) ⇒ the `set` throws `TypeError: immutable` out of the middleware ⇒ reds.
  it("TOOTH (immutable, poisoned): a redirect returned after a usr:-scoped read is stamped private — status and Location survive, and the middleware does not throw", async () => {
    const cache = makeCache();
    const { response, events } = await render(
      cache,
      async () => {
        await cache.getOrSet({
          factory: async () => "p",
          key: "profile",
          request: frameRequest(),
          tags: [t`user:1`],
        });
        // Immutable headers by construction — this is the production shape that 500s.
        return Response.redirect(REDIRECT_TO, 302);
      },
      "u-1"
    );
    // The redirect is intact…
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe(REDIRECT_TO);
    // …and the E23 posture landed on it anyway.
    expect(response.headers.get("cache-control")).toBe("private");
    expect(response.headers.get("cache-tag")).toBeNull();
    expect(l3Events(events)).toStrictEqual([
      { reason: "scope", type: L3_INELIGIBLE },
    ]);
  });

  it("TOOTH (immutable, unmanaged): a redirect from a render that consumed nothing still gets the safe default", async () => {
    const cache = makeCache();
    const { response } = await render(
      cache,
      async () => await Promise.resolve(Response.redirect(REDIRECT_TO, 307))
    );
    expect(response.status).toBe(307);
    expect(response.headers.get("location")).toBe(REDIRECT_TO);
    expect(response.headers.get("cache-control")).toBe("private");
  });

  it("TOOTH (immutable, eligible): a redirect from a public-only render carries the assembled Cache-Tag", async () => {
    const cache = makeCache();
    const { response } = await render(cache, async () => {
      await cache.getOrSet({
        factory: async () => "b",
        key: "book",
        scope: "public",
        tags: [t`book:1`],
      });
      return Response.redirect(REDIRECT_TO, 302);
    });
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe(REDIRECT_TO);
    expect(response.headers.get("cache-tag")).toBe("book:1");
  });

  // ── The SECOND immutable shape: a proxied `fetch()` response ────────────────────────────────
  // Same guard class as the redirect, and the same rescue catches it — but with a BODY, which is
  // the half a redirect cannot witness. Deterministic: a real `fetch()` of a `data:` URL is the
  // platform's own response object, so the guard under test is the genuine one rather than an
  // imitation, and there is no network, no server, and no timing anywhere in it.
  const DATA_URL = "data:text/plain,ok";

  /** A `fetch()` passthrough response, ASSERTED immutable on the way out — so that if a runtime
   * ever relaxed the guard, this tooth fails loudly instead of quietly testing nothing. */
  async function fetchedResponse(): Promise<Response> {
    const response = await fetch(DATA_URL);
    assert.throws(
      () => {
        response.headers.set("Cache-Control", "public");
      },
      TypeError,
      "the fetched response's headers are mutable — this tooth would be vacuous"
    );
    return response;
  }

  it("TOOTH (immutable, fetch passthrough — poisoned): a proxied fetch() response returned after a usr:-scoped read is stamped private, body intact, without throwing", async () => {
    const cache = makeCache();
    const { response, events } = await render(
      cache,
      async () => {
        await cache.getOrSet({
          factory: async () => "p",
          key: "profile",
          request: frameRequest(),
          tags: [t`user:1`],
        });
        return await fetchedResponse();
      },
      "u-1"
    );
    expect(response.status).toBe(200);
    // The rescue rebuilt the response — and a rebuild that lost the body would be no rescue at all.
    await expect(response.text()).resolves.toBe("ok");
    expect(response.headers.get("cache-control")).toBe("private");
    expect(response.headers.get("cache-tag")).toBeNull();
    expect(l3Events(events)).toStrictEqual([
      { reason: "scope", type: L3_INELIGIBLE },
    ]);
  });

  it("TOOTH (immutable, fetch passthrough — eligible): a proxied fetch() response from a public-only render carries the assembled Cache-Tag", async () => {
    const cache = makeCache();
    const { response } = await render(cache, async () => {
      await cache.getOrSet({
        factory: async () => "b",
        key: "book",
        scope: "public",
        tags: [t`book:1`],
      });
      return await fetchedResponse();
    });
    expect(response.status).toBe(200);
    await expect(response.text()).resolves.toBe("ok");
    expect(response.headers.get("cache-tag")).toBe("book:1");
    expect(response.headers.get("cache-control")).toBe("private");
  });
});

// ── The header BUDGET measures the header, separators and all (§7.2) ────────────────────────────
// The adapter emits the tag list JOINED by `,`, so a header carrying N tags costs the sum of the
// tags PLUS N−1 separator bytes. A budget that measures only the bare tags under-counts by exactly
// those N−1 bytes — it would pass a header that is genuinely over the limit, and an over-limit
// `Cache-Tag` is one a CDN may reject or truncate, which loses the purge handles the header exists
// to carry. The check has to measure the string that goes on the wire.
describe("§7.2 — the byte budget measures the joined header", () => {
  // Three 3-byte tags: 9 bytes bare, 11 bytes joined ("a:1,b:2,c:3").
  const TAGS = [t`a:1`, t`b:2`, t`c:3`];
  const BARE_BYTES = 9;
  const JOINED_BYTES = 11;

  // Mutation: measure the bare tags again (sum each tag's UTF-8 length, no separators) ⇒ 9 ≤ 10 ⇒
  // eligible ⇒ reds.
  it("TOOTH 11 (separators): a tag set that fits WITHOUT the separators but not WITH them is over budget", () => {
    const collector = makeCache().collect();
    for (const tag of TAGS) {
      collector.dependsOn(tag);
    }
    // A budget that the bare tags clear and the real header does not.
    const budget = BARE_BYTES + 1; // 10: ≥ 9 bare, < 11 joined
    const emission = collector.commitHeaders({ maxBytes: budget });
    expect(emission.eligible).toBeFalsy();
    expect(emission.ineligibleReason).toBe("budget");
  });

  it("CONTROL: the same tags are eligible at a budget that fits the JOINED header exactly", () => {
    const collector = makeCache().collect();
    for (const tag of TAGS) {
      collector.dependsOn(tag);
    }
    const emission = collector.commitHeaders({ maxBytes: JOINED_BYTES });
    expect(emission.eligible).toBeTruthy();
    expect(emission.cacheTags).toStrictEqual(["a:1", "b:2", "c:3"]);
  });
});

// ── The emitted header carries each purge handle ONCE (§7.2) ────────────────────────────────────
// Recording is per CONSUMPTION: an entry two loaders both read is recorded twice (a fill and a
// hit), so its tag reaches the collector twice. The BUDGET counts both — that is the §7.2 measure
// invariant 13 pins, and it fails closed — but the HEADER has no reason to repeat a purge handle.
// The dedup therefore lives at the adapter's join and nowhere else.
describe("§7.2 — the emitted Cache-Tag lists each tag once", () => {
  // Mutation: drop the `new Set(...)` from `decidePosture`'s eligible branch ⇒ the header comes out
  // `book:1,book:1` ⇒ reds.
  it("TOOTH 12 (dedup): one entry consumed twice in a render (a fill, then a hit) emits its tag once", async () => {
    const cache = makeCache();
    const read = async (): Promise<unknown> =>
      await cache.getOrSet({
        factory: async () => "b",
        key: "book",
        scope: "public",
        tags: [t`book:1`],
      });
    const { response } = await render(
      cache,
      loaderWith(async () => {
        await read(); // loader A — a FILL
        await read(); // loader B — a HIT on the same entry
      })
    );
    expect(response.headers.get("cache-tag")).toBe("book:1");
  });

  // The other half of the same guarantee, and the one that must NOT flip. The dedup above is safe
  // ONLY because the budget still counts every occurrence: emitting fewer bytes than were measured
  // is always safe, measuring fewer bytes than are emitted is not. Moving the dedup INTO
  // `commitHeaders` would collapse `["a","a","a","a"]` to one tag and let it pass a budget of 3 —
  // an over-budget tag list coming out eligible, which is the fail-OPEN direction (INV-13, §7.2).
  // Pinned here because the sacred INV-13 property test does not reliably generate the duplicate-
  // heavy shape that would catch it.
  it("TOOTH 12b (budget counts duplicates): the BUDGET is not deduplicated — an over-budget list of repeats stays ineligible", () => {
    const collector = makeCache().collect();
    for (let i = 0; i < 4; i++) {
      collector.dependsOn(t`a:1`); // the SAME tag, four times
    }
    const emission = collector.commitHeaders({ maxTags: 3 });
    expect(emission.eligible).toBeFalsy();
    expect(emission.ineligibleReason).toBe("budget");
  });
});

describe("the ambient-collector seam fails closed", () => {
  it("a cache driven with no middleware frame records nothing — a fresh collector sees zero dependencies", async () => {
    const cache = makeCache();
    await cache.getOrSet({
      factory: async () => "p",
      key: "profile",
      request: { userId: "u-1" },
      tags: [t`user:1`],
    });
    // No frame was open during that fill, so nothing reached any collector.
    const emission = cache.collect().commitHeaders();
    expect(managedDependenciesOf(emission)).toBe(0);
    expect(emission.eligible).toBeTruthy();
    expect(emission.cacheTags).toStrictEqual([]);
  });

  // The count rides an in-package WeakMap keyed by the emission, so it must actually arrive on the
  // emission the collector minted — a seam that silently returned 0 for every emission would make
  // the jurisdiction floor swallow every render (the adapter would then assert nothing, ever).
  it("the seam carries the count on the emission the collector minted", () => {
    const collector = makeCache().collect();
    collector.dependsOn(t`a:1`);
    collector.dependsOn(t`b:2`);
    expect(managedDependenciesOf(collector.commitHeaders())).toBe(2);
  });
});
