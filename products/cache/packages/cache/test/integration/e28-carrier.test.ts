// Ordinary unit teeth (NOT sacred) — E28's Ticks clause. The CARRIER: whatever this library ships as
// a composition has to bring its own freshness driver along. Each case bites ONE branch (rule 13):
//
//   1. THE CARRIER EXISTS — a plain `getOrSet` on a `createWorkersCache` instance drives a tick.
//      Before E28 the ONLY caller of a tick was the RR middleware, so a plain Worker on this factory
//      had no driver at all: no `__state` poll, no E5 feed, no redial ladder. Blind the carrier and
//      the tick count stays 0 ⇒ red.
//   2. THE FLOOR — E28 sets a minimum a carrier must clear: one tick to seed the cursor, and a
//      second one a jittered baseline cadence later. This proves the carrier actually clears it —
//      the later tick is the one that polls.
//   3. RATE-LIMITED — a burst of reads rides ONE tick. Blind the gate and 1000 reads/second hammer
//      the store ⇒ red.
//   4. ADOPT-DON'T-AWAIT — the read does not wait for the tick.
//   5. CANCELLATION-TOLERANT — a tick that REJECTS (which is how a request teardown surfaces: the
//      in-flight I/O rejects) never fails the read, never floats an unhandled rejection, and leaves
//      the reader BEHIND rather than falsely converged. This is the case E28 requires be
//      establishable before the carrier may ship.
//
// Lane: integration — because the real `createWorkersCache` config names workerd's binding types,
// which a Node-side lane cannot resolve (see the census's header for the same constraint). Nothing
// here is timing-TOLERANT despite the lane: the carrier reads the platform clock, so time is
// CONTROLLED with vitest's fake timers (verified working under this pool) rather than waited on.
// Fake bindings throughout — the subject is the driver, not the drivers it drives.

import { mirrorKeys } from "@astilba/cache/registry";
import { describe, expect, it, vi } from "vitest";

import { createWorkersCache } from "../../src/adapters/cloudflare/workers-cache";
import type { WorkersCacheConfig } from "../../src/adapters/cloudflare/workers-cache";
import type { TelemetryEvent } from "../../src/telemetry";

/** A KV namespace over a Map, with an optional injected read failure. */
function fakeKv(options: { failGet?: boolean } = {}): KVNamespace {
  const map = new Map<string, { value: string; metadata: unknown }>();
  const kv = {
    delete: async (key: string): Promise<void> => {
      map.delete(key);
    },
    get: async (key: string): Promise<string | null> => {
      if (options.failGet) {
        throw new Error("kv down (simulated)");
      }
      return map.get(key)?.value ?? null;
    },
    getWithMetadata: async (
      key: string
    ): Promise<{ value: string | null; metadata: unknown }> => {
      if (options.failGet) {
        throw new Error("kv down (simulated)");
      }
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
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a structural stand-in for the KVNamespace surface this adapter uses; the cases turn on the carrier, not on KV's full API.
  return kv as unknown as KVNamespace;
}

/** A Coordinator namespace whose bus dials are COUNTED and always fail — a dead bus. Every dial is
 * one `fetch` against a freshly-minted stub, so counting fetches counts dial ATTEMPTS. */
function dialCountingCoordinator(counter: { dials: number }): DurableObjectNamespace {
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
      counter.dials += 1;
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

/** A Coordinator namespace whose stub counts the `__state` reads a tick performs. */
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
      // No bus in this rig: the dial fails, which is itself the never-established path.
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
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a structural stand-in for the DurableObjectNamespace surface the adapter uses (idFromName + get).
  return namespace as unknown as DurableObjectNamespace;
}

/** The registry-scoped `__state` pointer key this rig's cache resyncs off — taken from the SAME
 * formatter the reader addresses with (`mirrorKeys`), never from a string guess. A hand-written
 * `"__state"` matcher silently counts nothing: the real key is `__reg:<regId>:state`, and a counter
 * that matches nothing reports zero ticks whether or not the carrier works. */
const STATE_KEY = mirrorKeys("carrier").stateKey;

/** A KV whose `__state` pointer reads are COUNTED — the observable a carrier tick produces. */
function countingKv(counter: { reads: number }): KVNamespace {
  const inner = fakeKv();
  return new Proxy(inner, {
    get(obj, prop, receiver) {
      const value = Reflect.get(obj, prop, receiver);
      if (
        (prop === "get" || prop === "getWithMetadata") &&
        typeof value === "function"
      ) {
        return (key: string, ...rest: unknown[]): unknown => {
          if (key === STATE_KEY) {
            counter.reads += 1;
          }
          // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- re-entering the wrapped KV's own method; the cast restores the callable type the Proxy `get` erased.
          return (value as (...a: unknown[]) => unknown).apply(obj, [
            key,
            ...rest,
          ]);
        };
      }
      return value;
    },
  });
}

function build(
  kv: KVNamespace,
  telemetry?: (event: TelemetryEvent) => void,
  coordinator: DurableObjectNamespace = fakeCoordinator()
): ReturnType<typeof createWorkersCache> {
  const config: WorkersCacheConfig = {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake stands in for the Coordinator-parameterized namespace; only idFromName/get are reached.
    coordinator: coordinator as unknown as WorkersCacheConfig["coordinator"],
    kv,
    name: "carrier",
    ...(telemetry === undefined ? {} : { telemetry }),
  };
  return createWorkersCache(config);
}

async function read(
  cache: ReturnType<typeof createWorkersCache>,
  key: string
): Promise<unknown> {
  return await cache.getOrSet({
    factory: async (): Promise<string> => "v",
    key,
  });
}

describe("E28 — the carrier: what ships as a composition brings its own freshness driver", () => {
  it("a plain getOrSet DRIVES a tick (before E28, only the RR middleware ever did)", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(0);
      const counter = { reads: 0 };
      const cache = build(countingKv(counter));

      await read(cache, "carrier/a");
      // Let the adopted (un-awaited) tick settle.
      await vi.advanceTimersByTimeAsync(0);

      // THE TOOTH: blind the carrier and this stays 0 — which is exactly what a plain Worker on this
      // factory had before E28. Its `__state` poll never ran, so nothing held its eventual reads
      // inside the staleness bound INV-25 fixes, and nothing said so.
      expect(counter.reads).toBeGreaterThan(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("THE FLOOR: two ticks a cadence apart — the first seeds the cursor, the second polls", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(0);
      const counter = { reads: 0 };
      const cache = build(countingKv(counter));

      await read(cache, "carrier/floor-1");
      await vi.advanceTimersByTimeAsync(0);
      const afterFirst = counter.reads;

      // A cadence later (the jittered 60s baseline; the rig's rng is the platform's, so a generous
      // step clears any jitter), a second read carries a second tick — the one that actually polls.
      vi.setSystemTime(120_000);
      await read(cache, "carrier/floor-2");
      await vi.advanceTimersByTimeAsync(0);

      // The floor is cleared: a seeding tick, then a polling one a cadence later. Blind the
      // carrier's clock read (or the gate's cursor) so the second tick never fires and this reds.
      expect(counter.reads).toBeGreaterThan(afterFirst);
    } finally {
      vi.useRealTimers();
    }
  });

  it("RATE-LIMITED: a burst of reads rides ONE tick", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(0);
      const counter = { reads: 0 };
      const cache = build(countingKv(counter));

      for (let i = 0; i < 50; i += 1) {
        await read(cache, `carrier/burst-${i}`);
      }
      await vi.advanceTimersByTimeAsync(0);

      // 50 reads inside one gate window cost ONE tick's worth of `__state` reads. Blind the gate and
      // this reds — a burst would hammer the store with a poll per read, which is the cost the
      // staleness gate exists to prevent.
      expect(counter.reads).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("CANCELLATION-TOLERANT: a REJECTING tick never fails the read and floats nothing", async () => {
    // THE CASE E28 REQUIRES BEFORE THE CARRIER MAY SHIP. On Workers an un-awaited promise is
    // cancelled when the response returns: the tick's in-flight I/O REJECTS. A failing KV models the
    // same shape — the tick's store read rejects mid-flight, exactly as a teardown makes it.
    vi.useFakeTimers();
    const floated: unknown[] = [];
    const onUnhandled = (error: unknown): void => {
      floated.push(error);
    };
    process.on("unhandledRejection", onUnhandled);
    try {
      vi.setSystemTime(0);
      const events: TelemetryEvent[] = [];
      const cache = build(fakeKv({ failGet: true }), (event) => {
        events.push(event);
      });

      // The READ SUCCEEDS. The tick's failure is not the read's business: a background freshness
      // probe must never fail a user's request, and the L2 read failing is separately absorbed to a
      // miss (E13), so the factory serves.
      await expect(read(cache, "carrier/cancel")).resolves.toBe("v");
      await vi.advanceTimersByTimeAsync(0);
      await Promise.resolve();

      // NOTHING FLOATED. The carrier catches everything — which is why the catch is total rather
      // than selective: a teardown's rejection is not distinguishable by type, and an unhandled
      // rejection here would be process-fatal on Node and an uncaught request error on workerd.
      expect(floated).toStrictEqual([]);

      // …and the failure was COUNTED rather than swallowed into silence.
      expect(
        events.filter((e) => e.type === "poll_tick_failed").length
      ).toBeGreaterThan(0);
    } finally {
      process.off("unhandledRejection", onUnhandled);
      vi.useRealTimers();
    }
  });

  it("THE REDIAL HALF: a dead bus + carrier ticks ⇒ dial attempts ADVANCE, and the ladder still paces them", async () => {
    // THE OTHER HALF OF THE TICKS CLAUSE, and until now the untested one. `run()` drives TWO things:
    // the poller (covered by the cases above) and `bus.tick`, which is the ONLY thing that ever
    // performs a due redial — the driver schedules on close and dials nothing by itself, because a
    // dial outside a request context is refused. Delete `bus.tick(nowMs)` and the redial ladder is
    // dead at birth again while every other case here stays green.
    vi.useFakeTimers();
    try {
      vi.setSystemTime(0);
      const counter = { dials: 0 };
      const cache = build(fakeKv(), undefined, dialCountingCoordinator(counter));

      // t=0 — the first read subscribes, which dials once. It fails (dead bus), and the driver
      // SCHEDULES the next attempt one base rung out rather than dialing again.
      await read(cache, "redial/0");
      await vi.advanceTimersByTimeAsync(0);
      expect(counter.dials).toBe(1);

      // t=2000 — past the first rung (1000ms ±20% jitter ⇒ due by 1200 at the latest), so this
      // read's tick performs the redial. Delete the tick call and this stays at 1 ⇒ red.
      vi.setSystemTime(2000);
      await read(cache, "redial/1");
      await vi.advanceTimersByTimeAsync(0);
      expect(counter.dials).toBe(2);

      // t=3000 — the SECOND rung is 2000ms out (±20% ⇒ due no earlier than 3600), so a tick here
      // must NOT dial. This is what proves the tick performs a DUE redial rather than dialing on
      // every tick: blind the due-check and a read-driven app would dial per request forever.
      vi.setSystemTime(3000);
      await read(cache, "redial/2");
      await vi.advanceTimersByTimeAsync(0);
      expect(counter.dials).toBe(2);

      // t=5000 — past that rung (due by 4400 at the latest) ⇒ the third attempt.
      vi.setSystemTime(5000);
      await read(cache, "redial/3");
      await vi.advanceTimersByTimeAsync(0);
      expect(counter.dials).toBe(3);

      // The gaps GROW (1s, 2s, 4s…): after the third failure the next rung is 4000ms out, so t=6000
      // is still too early. The ladder is a ladder, not a retry-per-read.
      vi.setSystemTime(6000);
      await read(cache, "redial/4");
      await vi.advanceTimersByTimeAsync(0);
      expect(counter.dials).toBe(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("CANCELLATION-TOLERANT: an interrupted tick leaves the reader BEHIND, never falsely converged", async () => {
    // The safety argument's core. A tick that dies mid-pass cannot leave the reader claiming more
    // knowledge than it has: watermark applies are per-frame atomic and monotone, and suspicion is
    // cleared only AFTER a replay completes. So the worst an interrupted tick can do is leave the
    // reader where it already was — which is the FAIL-CLOSED direction, costing a live check rather
    // than a stale serve.
    vi.useFakeTimers();
    try {
      vi.setSystemTime(0);
      const cache = build(fakeKv({ failGet: true }));
      await read(cache, "carrier/behind");
      await vi.advanceTimersByTimeAsync(0);

      const explanation = await cache.explain("carrier/behind");
      // Nothing was learned, and nothing was claimed: the applied epoch did not move, and the
      // channel (whose dial also failed in this rig) is honestly reported rather than assumed up.
      expect(explanation.reader.appliedEpoch).toBe(0);
      expect(explanation.reader.channel).toBe("never-established");
    } finally {
      vi.useRealTimers();
    }
  });
});
