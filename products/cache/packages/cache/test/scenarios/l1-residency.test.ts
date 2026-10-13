import { memory } from "@astilba/cache";
import { SimClock } from "@astilba/harness";
import { describe, expect, it } from "vitest";

import type { TelemetryEvent } from "../../src/telemetry";

describe("bounded L1 residency on an injected logical clock (§8, E13, MI12)", () => {
  it("expires at the exact residency boundary and preserves metadata before it", async () => {
    const clock = new SimClock(500);
    const store = memory({ clock });
    await store.set("a", "value", {
      expirationTtl: 2,
      metadata: { scope: "pub" },
    });
    clock.advance(1999);
    await expect(store.get("a")).resolves.toStrictEqual({
      metadata: { scope: "pub" },
      value: "value",
    });
    clock.advance(1);
    await expect(store.get("a")).resolves.toBeUndefined();
    await expect(store.get("missing")).resolves.toBeUndefined();
  });

  it("rejects a residency promise without a clock and preserves the old value", async () => {
    const store = memory();
    await store.set("a", "old");
    await expect(
      store.set("a", "new", { expirationTtl: 1 })
    ).rejects.toMatchObject({ name: "MemoryConfigError" });
    await expect(store.get("a")).resolves.toStrictEqual({ value: "old" });
  });

  it("read recency, not insertion order, chooses the entry-pressure victim", async () => {
    const store = memory({ maxEntries: 2 });
    await store.set("a", "a");
    await store.set("b", "b");
    await expect(store.get("a")).resolves.toStrictEqual({ value: "a" });
    await store.set("c", "c");
    await expect(store.get("b")).resolves.toBeUndefined();
    await expect(store.get("a")).resolves.toStrictEqual({ value: "a" });
    await expect(store.get("c")).resolves.toStrictEqual({ value: "c" });
  });

  it("replacement and deletion reclaim UTF-8 bytes without evicting unrelated entries", async () => {
    const store = memory({ maxBytes: 5 });
    await store.set("a", "é");
    await store.set("b", "abc");
    await expect(store.get("a")).resolves.toStrictEqual({ value: "é" });
    await store.set("a", "x");
    await store.set("c", "y");
    await expect(store.get("b")).resolves.toStrictEqual({ value: "abc" });
    await store.delete("a");
    await expect(store.get("a")).resolves.toBeUndefined();
    await store.delete("missing");
    await store.set("d", "z");
    await expect(
      Promise.all([
        store.get("a"),
        store.get("b"),
        store.get("c"),
        store.get("d"),
      ])
    ).resolves.toStrictEqual([
      undefined,
      { value: "abc" },
      { value: "y" },
      { value: "z" },
    ]);
  });

  it("oversize replacement fails before mutation, with a structural non-retryable error", async () => {
    const store = memory({ maxBytes: 2 });
    await store.set("a", "ok");
    await expect(store.set("a", "éx")).rejects.toMatchObject({
      code: "too_large",
      name: "MemoryOversizeError",
      retryable: false,
    });
    await expect(store.get("a")).resolves.toStrictEqual({ value: "ok" });
  });

  it("byte pressure evicts enough LRU entries and discloses only private counts and bytes", async () => {
    const events: TelemetryEvent[] = [];
    const store = memory({
      maxBytes: 4,
      telemetry: (event) => {
        events.push(event);
      },
    });
    await store.set("private-a", "é", { metadata: { scope: "usr:principal" } });
    await store.set("public-b", "bb", { metadata: { scope: "pub" } });
    await store.set("c", "ccc");
    await expect(store.get("private-a")).resolves.toBeUndefined();
    await expect(store.get("public-b")).resolves.toBeUndefined();
    await expect(store.get("c")).resolves.toStrictEqual({ value: "ccc" });
    expect(events).toStrictEqual([
      { bytes: 2, count: 1, type: "private_evicted" },
    ]);
  });

  it("a failed telemetry sink cannot prevent eviction or the successful write", async () => {
    const failures: unknown[] = [];
    const failure = new Error("sink failed");
    const store = memory({
      maxEntries: 1,
      telemetry: () => {
        throw failure;
      },
      onSinkError: (error) => {
        failures.push(error);
      },
    });
    await store.set("a", "a", { metadata: { scope: "usr:principal" } });
    await store.set("b", "b");
    await expect(store.get("a")).resolves.toBeUndefined();
    await expect(store.get("b")).resolves.toStrictEqual({ value: "b" });
    expect(failures).toStrictEqual([failure]);
  });
});
