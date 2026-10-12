// Ordinary ADAPTER-lane teeth (NOT sacred) — C4-b. The request-piggyback poll driver: the WeakMap
// seam read, the staleness gate, and the swallow wrap. Each case bites ONE branch (rule 13):
//   • seam read      — a registry-bearing cache HAS a poller and the tick reaches it; blind the read
//                      (return undefined) and the tick never fires;
//   • staleness gate — a burst of requests rides ONE tick; blind the gate (tick every request) and
//                      the second request ticks too ⇒ red;
//   • swallow wrap   — a THROWING tick leaves the response untouched AND increments the counter;
//                      remove the wrap and the rejection escapes into the waitUntil promise ⇒ red.
// Deterministic lane (rule 12): `tickIfStale` takes `nowMs` explicitly, so the gate is proven with no
// wall clock and no timing tolerance. The middleware cases use one fresh cache each, so the gate's
// cursor (a WeakMap keyed by the instance) starts empty and the first request always ticks.

import assert from "node:assert/strict";
import { setImmediate } from "node:timers/promises";

import { createCache } from "@astilba/cache";
import type {
  Bus,
  BusEvent,
  Cache,
  Registry,
  RegistryAck,
  Store,
  StoreValue,
  Subscription,
  TagKnowledge,
  TelemetryEvent,
} from "@astilba/cache";
import {
  cacheMiddleware,
  POLL_TICK_FAILED,
  TICK_MIN_INTERVAL_MS,
} from "@astilba/cache/react-router";
import type { CacheMiddlewareArgs } from "@astilba/cache/react-router";
import { RouterContextProvider } from "react-router";
import { describe, expect, it } from "vitest";

import { tickIfStale } from "../../src/adapters/react-router/poll-driver";

/** A store whose `get` either serves from a map or REJECTS — the poller's `__state` read is the
 * first thing every tick does, so a rejecting `get` is a rejecting tick. */
class ProbeStore implements Store {
  reads = 0;
  constructor(private readonly failGet = false) {}

  async get(_key: string): Promise<StoreValue | undefined> {
    this.reads += 1;
    if (this.failGet) {
      throw new Error("l2 down");
    }
    return undefined;
  }

  async set(_key: string, _value: string): Promise<void> {
    // The poll path never writes.
  }

  async delete(_key: string): Promise<void> {
    // The poll path never deletes.
  }
}

/** The minimum `Registry` a registry+bus+l2 instance needs to construct (the poller is built under
 * exactly that condition). No mutation is driven here — only the poll path. */
const stubRegistry: Registry = {
  regId: "rr-driver",
  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    return tags.map((): TagKnowledge => ({ known: false }));
  },
  async expire(): Promise<RegistryAck> {
    return { epoch: 0 };
  },
  async delete(): Promise<RegistryAck> {
    return { epoch: 0 };
  },
  async registerRetention(): Promise<void> {
    // No lease in this lane.
  },
};

/** A silent bus: it subscribes and never delivers (the reader converges reactively off the mirror,
 * which is what the poll tick drives). */
const silentBus: Bus = {
  subscribe(_onEvent: (event: BusEvent) => void): Subscription {
    return { close: (): void => undefined };
  },
};

/** A cache with a registry + bus + L2 — the condition under which `createCache` builds (and holds)
 * the replication poller the driver reads back off the WeakMap seam. */
function makeCache(store: Store): Cache {
  return createCache({
    namespace: "rr-driver",
    clock: { now: () => 0 },
    rng: { next: () => 0.5 },
    l2: store,
    registry: stubRegistry,
    bus: silentBus,
  });
}

/** A driverless cache (no registry/bus/l2) — no reader, therefore no poller: the tick must no-op. */
function makeDriverlessCache(): Cache {
  return createCache({
    namespace: "rr-driverless",
    clock: { now: () => 0 },
    rng: { next: () => 0 },
  });
}

function makeArgs(context: RouterContextProvider): CacheMiddlewareArgs {
  return {
    context,
    params: {},
    pattern: "/",
    request: new Request("https://example.test/x"),
    url: new URL("https://example.test/x"),
  };
}

describe("request-piggyback poll driver — the WeakMap seam + the staleness gate (C4-b, §4.2)", () => {
  it("reads the poller off the seam and ticks it: the __state observation fires (blind the seam ⇒ never ticks)", async () => {
    const store = new ProbeStore();
    const cache = makeCache(store);

    const tick = tickIfStale(cache, 0);
    expect(tick).toBeDefined();
    await tick;

    // One tick = one `__state` read (the E5 feed + the ladder + the baseline poll all hang off it).
    // With `replicationPollerOf` blinded to `undefined`, `tickIfStale` returns undefined and this is 0.
    expect(store.reads).toBe(1);
  });

  it("a registry-absent cache has no poller — the tick is a no-op, never a throw", async () => {
    const tick = tickIfStale(makeDriverlessCache(), 0);
    expect(tick).toBeUndefined();
  });

  it("gates a burst: requests inside the tick interval ride ONE tick (blind the gate ⇒ two reads)", async () => {
    const store = new ProbeStore();
    const cache = makeCache(store);

    await tickIfStale(cache, 0);
    expect(store.reads).toBe(1);

    // Two more requests INSIDE the floor (1ms later, and one ms below it): the gate holds both —
    // nothing is due, since every cadence the poller owns is longer than this floor. Blind the gate
    // (tick every request) and `reads` climbs to 3 ⇒ red.
    const gated = [
      tickIfStale(cache, 1),
      tickIfStale(cache, TICK_MIN_INTERVAL_MS - 1),
    ];
    expect(gated).toStrictEqual([undefined, undefined]);
    expect(store.reads).toBe(1);

    // AT the floor — the tick is stale, so it fires again.
    await tickIfStale(cache, TICK_MIN_INTERVAL_MS);
    expect(store.reads).toBe(2);
  });

  it("gates per cache instance — two caches in one isolate never starve each other's ticks", async () => {
    const storeA = new ProbeStore();
    const storeB = new ProbeStore();
    await tickIfStale(makeCache(storeA), 0);
    await tickIfStale(makeCache(storeB), 0);
    expect(storeA.reads).toBe(1);
    expect(storeB.reads).toBe(1);
  });
});

describe("request-piggyback poll driver — the SWALLOW wrap (C4-b, rule 13)", () => {
  it("a THROWING tick never reaches the response, and increments the counter (remove the wrap ⇒ the rejection escapes)", async () => {
    // The scene: L2 is down, so the tick the middleware fires at request start REJECTS.
    const cache = makeCache(new ProbeStore(true));
    const events: TelemetryEvent[] = [];
    // The app's `ctx.waitUntil`: it COLLECTS the promise, exactly as the Workers runtime holds it
    // alive past the response — and so it is also what would observe an escaping rejection.
    const held: Promise<unknown>[] = [];
    const middleware = cacheMiddleware({
      cache,
      telemetry: (event) => {
        events.push(event);
      },
      waitUntil: (promise) => {
        held.push(promise);
      },
    });

    // 1) The RESPONSE is unaffected: the middleware resolves normally and still stamps its header.
    const response = await middleware(
      makeArgs(new RouterContextProvider()),
      async () => new Response("ok")
    );
    // `assert.ok` narrows to Response without a cast (and fails if it isn't one).
    assert.ok(response instanceof Response);
    expect(response.headers.get("cache-control")).toBe("private");

    // 2) The handed-off promise RESOLVES — the wrap ate the rejection. Remove the wrap (hand
    //    `poller.onPollTick(now)` to waitUntil raw) and this `Promise.all` REJECTS ⇒ red. This is the
    //    escape the Workers runtime would surface as an unhandled rejection in the request lifecycle.
    expect(held).toHaveLength(1);
    await expect(Promise.all(held)).resolves.toBeDefined();

    // 3) The failure is COUNTED, not silenced: the swallow is paired with the telemetry event.
    expect(events).toStrictEqual([{ error: "Error", type: POLL_TICK_FAILED }]);
  });

  it("fires the tick at request start through waitUntil — and never awaits it on the request path", async () => {
    const store = new ProbeStore();
    const cache = makeCache(store);
    const held: Promise<unknown>[] = [];
    const middleware = cacheMiddleware({
      cache,
      waitUntil: (promise) => {
        held.push(promise);
      },
    });

    let readsInsideLoader = -1;
    await middleware(makeArgs(new RouterContextProvider()), async () => {
      // The tick was ALREADY fired when the loader runs (request start, not request end) — the
      // `__state` read is in flight or done, and the loader never waited for it.
      readsInsideLoader = store.reads;
      return new Response("ok");
    });

    expect(readsInsideLoader).toBe(1);
    expect(held).toHaveLength(1);
    await Promise.all(held);
  });

  it("without a configured waitUntil the tick still fires (best-effort) and the response is unchanged", async () => {
    const store = new ProbeStore();
    const middleware = cacheMiddleware({ cache: makeCache(store) });
    const response = await middleware(
      makeArgs(new RouterContextProvider()),
      async () => new Response("ok")
    );
    expect(store.reads).toBe(1);
    assert.ok(response instanceof Response);
    expect(response.headers.get("cache-control")).toBe("private");
  });
});

// ── F2: the swallow wrap's OWN escape hatch (C4-b review, rule 13) ────────────────────────────────
// The wrap above proves a failing TICK cannot reach the response. It proved nothing about the sink
// the wrap reports THROUGH — app code, called from inside the very catch block whose job is to not
// throw. Two failure modes, both of which turned the swallow back into an escape:
//   • a sink that THROWS synchronously      ⇒ the catch block throws ⇒ the promise documented as
//     "NEVER rejects" rejects ⇒ and the default `waitUntil` is `void promise` ⇒ UNHANDLED REJECTION
//     (process-fatal on the Node/RR lane, an uncaught request error on workerd);
//   • an `async` sink that REJECTS          ⇒ type-checks against `TelemetrySink` ((e) => void
//     accepts any return), and its rejected promise floats away unadopted ⇒ UNHANDLED REJECTION.
// Both must leave the tick promise RESOLVED and float nothing. Mutation: drop the `emitSwallowed`
// guard (call `telemetry?.(...)` raw in `runTick`'s catch) ⇒ BOTH of these red.

/** The tick's rejection path is only reachable through a failing store, so both cases below run
 * against an L2 that is down (`ProbeStore(true)`) — the sink is then called for real. */
function makeFailingTickCache(): Cache {
  return makeCache(new ProbeStore(true));
}

/** An `async` telemetry sink whose transport is down. It type-checks against `TelemetrySink`
 * (`(event) => void` — a `void` return type accepts ANY returned value), so nothing stops an app
 * from shipping it; what it hands the driver is a REJECTING PROMISE the driver never asked for. */
async function rejectingAsyncSink(): Promise<void> {
  await Promise.resolve();
  throw new Error("sink transport is down");
}

/**
 * Run `body` while capturing PROCESS-LEVEL unhandled rejections — the failure an `async` sink's
 * rejected promise causes, which is invisible to a plain `await` on the tick (the tick resolves; the
 * sink's promise floats away separately). Node reports an un-adopted rejection at the end of the
 * turn, so the macrotask hop below is what gives it the chance to fire.
 */
async function unhandledRejectionsDuring(
  body: () => Promise<void>
): Promise<unknown[]> {
  const escaped: unknown[] = [];
  const onUnhandled = (reason: unknown): void => {
    escaped.push(reason);
  };
  process.on("unhandledRejection", onUnhandled);
  try {
    await body();
    await setImmediate(); // one macrotask turn: Node's unhandled-rejection sweep runs here
    return escaped;
  } finally {
    process.off("unhandledRejection", onUnhandled);
  }
}

describe("request-piggyback poll driver — the swallow wrap's OWN escape hatch (C4-b, F2)", () => {
  it("a THROWING telemetry sink cannot reject the tick (drop the guard ⇒ the catch block throws ⇒ red)", async () => {
    const escaped = await unhandledRejectionsDuring(async () => {
      // The sink an app actually ships one day: it throws (a bad JSON.stringify, a dead transport).
      const tick = tickIfStale(makeFailingTickCache(), 0, () => {
        throw new Error("sink is broken");
      });
      expect(tick).toBeDefined();
      // THE CONTRACT: still resolved. Without the guard, `runTick`'s catch block re-throws the
      // sink's error, this promise REJECTS, and the default `waitUntil` (`void promise`) turns it
      // into an unhandled rejection ⇒ this line reds.
      await expect(tick).resolves.toBeUndefined();
    });
    expect(escaped).toStrictEqual([]);
  });

  it("a REJECTING async telemetry sink floats NOTHING (drop the guard ⇒ an unhandled rejection escapes ⇒ red)", async () => {
    const escaped = await unhandledRejectionsDuring(async () => {
      // oxlint-disable-next-line typescript/no-misused-promises, typescript/strict-void-return -- DELIBERATE: this IS the defect under test. `TelemetrySink` is `(event) => void`, and TypeScript ACCEPTS an async function there (a `void` return type accepts any returned value), so a user can ship exactly this and the COMPILER will not stop them. These two lint rules would — but a consumer of this library is not obliged to run our linter, so the driver has to survive it. That is what this case proves.
      const tick = tickIfStale(makeFailingTickCache(), 0, rejectingAsyncSink);
      expect(tick).toBeDefined();
      // The tick itself resolves either way — that is exactly why this mode was invisible.
      await expect(tick).resolves.toBeUndefined();
    });
    // THE CONTRACT: the sink's rejection was ADOPTED and swallowed, so nothing floated. Without the
    // guard the raw `telemetry?.(...)` call drops a rejected promise on the floor and this reds.
    expect(escaped).toStrictEqual([]);
  });

  it("a well-behaved sink is unaffected: the event is still delivered exactly once", async () => {
    // The guard must not cost the counter it exists to protect (§10) — a non-vacuity check on the
    // two cases above: the emit still happens, still once, still with the error's NAME only.
    const events: TelemetryEvent[] = [];
    await tickIfStale(makeFailingTickCache(), 0, (event) => {
      events.push(event);
    });
    expect(events).toStrictEqual([{ error: "Error", type: POLL_TICK_FAILED }]);
  });
});
