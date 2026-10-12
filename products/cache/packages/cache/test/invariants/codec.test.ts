// INV-8  — spec §11.8
// INV-27 — errata E8 (§11 invariant amendment)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { createCache } from "@astilba/cache";
import type { Cache, Codec, Store } from "@astilba/cache";
import { FakeStore, mkTag, SimClock, SimRng } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

const jsonCodec: Codec = {
  decode: (value) => JSON.parse(value) as unknown,
  encode: (value) => JSON.stringify(value),
  id: "json/1",
};
const altCodec: Codec = {
  decode: (value) => JSON.parse(value) as unknown,
  encode: (value) => JSON.stringify(value),
  id: "alt/1",
};
const poisonCodec: Codec = {
  decode: () => {
    throw new Error("decode failed");
  },
  encode: (value) => JSON.stringify(value),
  id: "json/1", // same id, but decode throws — models corrupt/unknown encoding,
};

function cacheWith(codec: Codec, l2: Store, clock: SimClock): Cache {
  return createCache({ clock, codec, l2, namespace: "t", rng: new SimRng(1) });
}

describe("codec", () => {
  test("INV-8: a codec round-trips byte-stable within a version", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const value = { a: 1, b: [2, 3], c: "x" };
    const writer = cacheWith(jsonCodec, store.replica(0), clock);
    const written = await writer.getOrSet({
      factory: async () => await Promise.resolve(value),
      key: "k",
      tags: [mkTag("t0")],
    });
    expect(written).toStrictEqual(value);
    // A fresh cache with the SAME codec decodes the durable bytes identically.
    const reader = cacheWith(jsonCodec, store.replica(0), clock);
    const read = await reader.getOrSet({
      factory: async () => await Promise.resolve({ fresh: true }),
      key: "k",
      tags: [mkTag("t0")],
    });
    expect(read).toStrictEqual(value);
  });

  test("INV-8: a decode failure fails closed to a miss (refill), never a mis-typed serve", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const writer = cacheWith(jsonCodec, store.replica(0), clock);
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [mkTag("t0")],
    });
    const reader = cacheWith(poisonCodec, store.replica(0), clock);
    const read = await reader.getOrSet({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [mkTag("t0")],
    });
    expect(read).toBe("v2"); // decode failure ⇒ miss ⇒ refill
  });

  test("INV-27: a codecId mismatch fails closed to miss unless in acceptCodecs (E8)", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const writer = cacheWith(jsonCodec, store.replica(0), clock);
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [mkTag("t0")],
    });
    // A cache with a different codec id must fail closed to a miss, never mis-decode.
    const reader = cacheWith(altCodec, store.replica(0), clock);
    const read = await reader.getOrSet({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [mkTag("t0")],
    });
    expect(read).toBe("v2"); // codecId mismatch ⇒ miss ⇒ refill
  });

  test.prop([fc.jsonValue()])(
    "INV-8 INV-27 property: same-codec round-trip returns the value; a mismatched codec refills",
    async (value) => {
      const clock = new SimClock();
      const store = new FakeStore(clock, { replicas: 1 });
      const writer = cacheWith(jsonCodec, store.replica(0), clock);
      await writer.getOrSet({
        factory: async () => await Promise.resolve(value),
        key: "k",
        tags: [mkTag("t0")],
      });
      const reader = cacheWith(altCodec, store.replica(0), clock);
      const refilled = await reader.getOrSet({
        factory: async () => await Promise.resolve("REFILL"),
        key: "k",
        tags: [mkTag("t0")],
      });
      expect(refilled).toBe("REFILL");
    }
  );
});
