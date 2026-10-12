import { describe, expect, test } from "vitest";

import { SimClock } from "./clock";
import { FakeStore, WriteThrottleError } from "./store";

// Harness self-tests (P1) — FakeStore replica-lag visibility, TTL, throttle.

describe(FakeStore, () => {
  test("single view is strongly consistent (immediate visibility)", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const s = store.replica(0);
    await s.set("k", "v1");
    await expect(s.get("k")).resolves.toStrictEqual({ value: "v1" });
  });

  test("replica lag hides a write until the delay elapses", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 2 });
    store.delayReplica(1, 100);
    await store.replica(0).set("k", "v1");
    await expect(store.replica(0).get("k")).resolves.toStrictEqual({
      value: "v1",
    });
    await expect(store.replica(1).get("k")).resolves.toBeUndefined();
    clock.advance(100);
    await expect(store.replica(1).get("k")).resolves.toStrictEqual({
      value: "v1",
    });
  });

  test("a lagging replica can still observe an older version", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 2 });
    await store.replica(0).set("k", "v1");
    clock.advance(1000);
    store.delayKey("k", 500);
    await store.replica(0).set("k", "v2");
    // replica 1 sees v1 (visible) but not yet v2 (written at t=1000, +500 delay).
    await expect(store.replica(1).get("k")).resolves.toStrictEqual({
      value: "v1",
    });
    clock.advance(500);
    await expect(store.replica(1).get("k")).resolves.toStrictEqual({
      value: "v2",
    });
  });

  test("TTL expiry via SimClock", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const s = store.replica(0);
    await s.set("k", "v", { expirationTtl: 60 });
    clock.advance(59_000);
    await expect(s.get("k")).resolves.toStrictEqual({ value: "v" });
    clock.advance(1000);
    await expect(s.get("k")).resolves.toBeUndefined();
  });

  test("forced throttle rejects a rapid write (429)", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    store.throttleKey("k");
    await expect(store.replica(0).set("k", "v")).rejects.toBeInstanceOf(
      WriteThrottleError
    );
  });

  test("delete tombstones the key", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const s = store.replica(0);
    await s.set("k", "v");
    await s.delete("k");
    await expect(s.get("k")).resolves.toBeUndefined();
  });
});
