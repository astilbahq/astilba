import type { BusEvent } from "@astilba/cache";
import { describe, expect, test } from "vitest";

import { FakeBus } from "./bus";
import { SimClock } from "./clock";
import { FakeStore, InjectedStoreError, WriteThrottleError } from "./store";

// Knob-liveness guard (spirit): every chaos knob must have an OBSERVABLE
// effect on the system. A knob silently reduced to a no-op must RED here, not rot into a
// dead switch that makes a trace's chaos vacuous. One guard per knob family — delayKey /
// throttleKey / injectGetFailure / injectSetFailure (store) and dropNextFrames /
// disconnect / reconnect (bus). Each guard also observes the knob's OFF state so the
// assertion cannot pass vacuously.

const emptyFrame = (fromEpoch: number, toEpoch: number) => ({
  changes: [],
  fromEpoch,
  toEpoch,
});

describe("chaos-knob liveness (store)", () => {
  test("delayKey hides a write until the delay elapses", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    store.delayKey("k", 100);
    await store.replica(0).set("k", "v");
    // ON: not yet visible within the delay window.
    await expect(store.replica(0).get("k")).resolves.toBeUndefined();
    // OFF (delay elapsed): visible again — the knob only delays, it does not drop.
    clock.advance(100);
    await expect(store.replica(0).get("k")).resolves.toStrictEqual({
      value: "v",
    });
  });

  test("throttleKey rejects the next write (429)", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    // OFF: an un-throttled write lands.
    await store.replica(0).set("k", "v");
    // ON: the forced throttle rejects the next write.
    store.throttleKey("k");
    await expect(store.replica(0).set("k", "v2")).rejects.toBeInstanceOf(
      WriteThrottleError
    );
  });

  test("injectGetFailure makes reads of the key throw", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    await store.replica(0).set("k", "v");
    // OFF: the read succeeds.
    await expect(store.replica(0).get("k")).resolves.toStrictEqual({
      value: "v",
    });
    // ON: the injected failure surfaces as a rejected read.
    store.injectGetFailure("k");
    await expect(store.replica(0).get("k")).rejects.toBeInstanceOf(
      InjectedStoreError
    );
  });

  test("injectSetFailure makes writes of the key throw", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    // OFF: the write lands.
    await store.replica(0).set("k", "v");
    // ON: the injected failure surfaces as a rejected write.
    store.injectSetFailure("k");
    await expect(store.replica(0).set("k", "v2")).rejects.toBeInstanceOf(
      InjectedStoreError
    );
  });
});

describe("chaos-knob liveness (bus)", () => {
  test("dropNextFrames turns the next published frame into a gap (never delivered)", () => {
    const clock = new SimClock();
    const bus = new FakeBus(clock);
    const events: BusEvent[] = [];
    bus.subscribe((e) => {
      events.push(e);
    }); // initial reset
    bus.dropNextFrames(1);
    bus.publish(emptyFrame(0, 1));
    bus.deliverDue(clock.now());
    expect(events.some((e) => e.kind === "gap")).toBeTruthy();
    expect(events.some((e) => e.kind === "frame")).toBeFalsy();
  });

  test("disconnect severs delivery (frames published while down are lost)", () => {
    const clock = new SimClock();
    const bus = new FakeBus(clock);
    const events: BusEvent[] = [];
    bus.subscribe((e) => {
      events.push(e);
    }); // initial reset
    // OFF: a connected publish delivers (non-vacuous baseline).
    bus.publish(emptyFrame(0, 1));
    expect(events.filter((e) => e.kind === "frame")).toHaveLength(1);
    // ON: a publish while disconnected is lost.
    bus.disconnect();
    bus.publish(emptyFrame(1, 2));
    bus.deliverDue(clock.now());
    expect(events.filter((e) => e.kind === "frame")).toHaveLength(1);
  });

  test("reconnect re-establishes the transport (emits reset, restores delivery)", () => {
    const clock = new SimClock();
    const bus = new FakeBus(clock);
    const events: BusEvent[] = [];
    bus.subscribe((e) => {
      events.push(e);
    }); // initial reset (1)
    bus.disconnect();
    bus.reconnect(); // reset (2)
    expect(events.filter((e) => e.kind === "reset")).toHaveLength(2);
    // Delivery is restored: a frame published after reconnect arrives.
    bus.publish(emptyFrame(0, 1));
    expect(events.some((e) => e.kind === "frame")).toBeTruthy();
  });
});
