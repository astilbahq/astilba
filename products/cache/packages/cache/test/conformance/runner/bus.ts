import assert from "node:assert/strict";

import type { Bus, BusEvent, BusFrame, TagChange } from "@astilba/cache";
import { describe, it } from "vitest";

/* References: §4.2, E4, E21, E19. */

export interface MismatchProbe {
  readonly events: readonly BusEvent[];
  readonly closed: boolean;
}

interface DoubtObservation {
  readonly frames: readonly BusFrame[];
  readonly closed: boolean;
}

export interface DoubtProbe {
  readonly doubted: DoubtObservation;
  readonly healthy: DoubtObservation;
}

/** What a Bus implementation factory returns for the contract to drive. Only `bus` and `publish`
 * are mandatory; a driver that cannot inject a given control deterministically leaves it absent and
 * the affected check SELF-SKIPS to integration (lane law). */
export interface BusHarness {
  readonly bus: Bus;
  /** Registry side: broadcast a frame (subject to the harness's own chaos). */
  publish(frame: BusFrame): void;
  /** Re-establish the transport; a conformant bus re-emits `reset` → `hello` before any frame. */
  reconnect?(): void;
  /** Drop the next `n` published frames, creating a hole the bus must signal as a `gap` (B2). */
  dropNext?(n: number): void;
  /** Deliver any buffered frames whose time has come (deterministic pump). Absent for a bus that
   * delivers synchronously on publish (the FakeBus reference at zero delay; the doBus pair). */
  pump?(): void;
  /** Subscribe a client whose configured scope MISMATCHES the channel's, resolving with the events
   * it received + whether the transport closed it (③, E19). Absent on a bus with no scope echo (the
   * FakeBus) — the regId-echo check self-skips. */
  subscribeMismatchedScope?(): Promise<MismatchProbe>;

  deliveryDoubtProbe?(frame: BusFrame): Promise<DoubtProbe>;
}

export interface BusContractOptions {
  readonly label?: string;
}

const mkFrame = (
  fromEpoch: number,
  toEpoch: number,
  changes: readonly TagChange[] = []
): BusFrame => ({ changes, fromEpoch, toEpoch });

/** Narrow the frame payloads out of a mixed event stream (type-safe filter). */
const framesOf = (events: readonly BusEvent[]): readonly BusFrame[] =>
  events.flatMap((e) => (e.kind === "frame" ? [e.frame] : []));

const kindsOf = (events: readonly BusEvent[]): readonly BusEvent["kind"][] =>
  events.map((e) => e.kind);

/**
 * B1 — every transport establishment emits `reset` before any frame. The initial
 * subscribe is one establishment; a `reconnect()` is another. A bus that re-opens the
 * transport without re-issuing `reset` leaves a subscriber trusting knowledge it may
 * have missed a purge behind (E4) — this check rejects it.
 */
const checkResetBeforeFrame = async (make: () => BusHarness): Promise<void> => {
  const h = make();
  const events: BusEvent[] = [];
  const sub = h.bus.subscribe((e) => {
    events.push(e);
  });
  try {
    // Establishment #1 (subscribe): the FIRST delivered event must be `reset`.
    assert.ok(
      events.length >= 1,
      "B1: establishing a subscription must deliver an event"
    );
    assert.strictEqual(
      events[0]?.kind,
      "reset",
      "B1: the first event on establishment must be reset, before any frame"
    );
    // A frame after establishment lands as a frame, ordered after the reset.
    h.publish(mkFrame(0, 1));
    h.pump?.();

    if (h.reconnect) {
      // Establishment #2 (reconnect): a fresh `reset` must precede any later frame.
      const mark = events.length;
      h.reconnect();
      h.publish(mkFrame(1, 2));
      h.pump?.();
      const afterReconnect = events.slice(mark);
      assert.strictEqual(
        afterReconnect[0]?.kind,
        "reset",
        "B1: reconnect must re-emit reset before any later frame"
      );
      const resetIdx = afterReconnect.findIndex((e) => e.kind === "reset");
      const frameIdx = afterReconnect.findIndex((e) => e.kind === "frame");
      assert.ok(
        frameIdx === -1 || resetIdx < frameIdx,
        "B1: no frame may precede the reconnect reset"
      );
    }
  } finally {
    sub.close();
  }
};

/**
 * B1′ — every transport establishment emits `reset` → `hello` → frames (E21). The `hello`
 * follows the `reset` and precedes any frame, on the initial subscribe AND on a reconnect.
 * A bus that re-opens the transport without a `hello` leaves the reader with no head to
 * anchor convergence on — a caught-up subscriber can never re-trust its warm map (the E5
 * heartbeat is no longer an anchor, E21) — so this check rejects it.
 */
const checkHelloAfterReset = async (make: () => BusHarness): Promise<void> => {
  const h = make();
  const events: BusEvent[] = [];
  const sub = h.bus.subscribe((e) => {
    events.push(e);
  });
  try {
    // Establishment #1 (subscribe): reset THEN hello, both before any frame.
    assert.ok(
      events.length >= 2,
      "B1′: establishing a subscription must deliver reset then hello"
    );
    assert.strictEqual(
      events[0]?.kind,
      "reset",
      "B1′: the first event on establishment must be reset"
    );
    assert.strictEqual(
      events[1]?.kind,
      "hello",
      "B1′: hello must immediately follow reset, before any frame"
    );
    // A frame after establishment lands after the hello.
    h.publish(mkFrame(0, 1));
    h.pump?.();

    if (h.reconnect) {
      // Establishment #2 (reconnect): a fresh `reset` → `hello` must precede any later frame.
      const mark = events.length;
      h.reconnect();
      h.publish(mkFrame(1, 2));
      h.pump?.();
      const after = events.slice(mark);
      assert.strictEqual(
        after[0]?.kind,
        "reset",
        "B1′: reconnect must re-emit reset first"
      );
      assert.strictEqual(
        after[1]?.kind,
        "hello",
        "B1′: reconnect must emit hello after reset, before any later frame"
      );
      const helloIdx = after.findIndex((e) => e.kind === "hello");
      const frameIdx = after.findIndex((e) => e.kind === "frame");
      assert.ok(
        frameIdx === -1 || helloIdx < frameIdx,
        "B1′: no frame may precede the reconnect hello"
      );
    }
  } finally {
    sub.close();
  }
};

/* References: E21. */

const checkHelloHeadLowerBound = async (
  make: () => BusHarness
): Promise<void> => {
  const h = make();
  if (!h.reconnect) {
    // No deterministic re-establishment (real driver) — deferred to integration.
    return;
  }
  const events: BusEvent[] = [];
  const sub = h.bus.subscribe((e) => {
    events.push(e);
  });
  try {
    // Advance the channel: broadcast frames up through toEpoch 3.
    h.publish(mkFrame(0, 1));
    h.publish(mkFrame(1, 2));
    h.publish(mkFrame(2, 3));
    h.pump?.();
    const maxToEpoch = Math.max(0, ...framesOf(events).map((f) => f.toEpoch));
    // Re-establish and read the fresh hello: its head must be ≥ every frame previously emitted.
    const mark = events.length;
    h.reconnect();
    h.pump?.();
    const hello = events.slice(mark).find((e) => e.kind === "hello");
    assert.ok(hello, "B1′ lower bound: reconnect must emit a hello");
    assert.ok(
      hello.kind === "hello" && hello.head >= maxToEpoch,
      `B1′ lower bound: hello head must be ≥ every previously-emitted frame toEpoch (${maxToEpoch})`
    );
  } finally {
    sub.close();
  }
};

/**
 * B2 — a dropped/lost frame is signaled as a `gap`, never silently skipped. Post-reset
 * delivery is contiguous from the applied epoch, OR the break is announced so the
 * subscriber can resync. Requires a loss-injection control (`dropNext`); without one
 * the property is an integration-lane assertion (lane law).
 */
const checkGapOnLoss = async (make: () => BusHarness): Promise<void> => {
  const h = make();
  if (!h.dropNext) {
    // No deterministic loss injection (real driver) — deferred to integration.
    return;
  }
  const events: BusEvent[] = [];
  const sub = h.bus.subscribe((e) => {
    events.push(e);
  });
  try {
    // A contiguous frame flows through normally.
    h.publish(mkFrame(0, 1));
    h.pump?.();
    // Now lose the next frame: the transport must signal `gap`, not swallow it (B2).
    h.dropNext(1);
    h.publish(mkFrame(1, 2)); // lost
    h.publish(mkFrame(2, 3)); // delivered after the hole
    h.pump?.();
    assert.ok(
      kindsOf(events).includes("gap"),
      "B2′: a dropped/lost frame must be signaled as a gap"
    );
  } finally {
    sub.close();
  }
};

/**
 * B2′ lower bound — a `gap`'s `head` is ≥ the `toEpoch` of every frame emitted before the signal
 * (E21). Stated implementation-agnostically: advance the channel with delivered frames, drop a
 * higher one, and assert the resulting `gap` declares a head no lower than the highest frame the
 * subscriber saw. Delivery ≠ truth — the dropped frame still advanced the channel — so a
 * conformant bus can only OVER-declare (`head` ≥ the dropped epoch too). A bus that under-declares
 * would let a reader that reaches the highest delivered frame believe it had accounted for the
 * hole. Requires a loss-injection control (`dropNext`); without one it falls to integration.
 */
const checkGapHeadLowerBound = async (
  make: () => BusHarness
): Promise<void> => {
  const h = make();
  if (!h.dropNext) {
    // No deterministic loss injection (real driver) — deferred to integration.
    return;
  }
  const events: BusEvent[] = [];
  const sub = h.bus.subscribe((e) => {
    events.push(e);
  });
  try {
    // Deliver frames up through toEpoch 2, then DROP a higher one (toEpoch 3).
    h.publish(mkFrame(0, 1));
    h.publish(mkFrame(1, 2));
    h.pump?.();
    h.dropNext(1);
    h.publish(mkFrame(2, 3)); // lost → gap
    h.pump?.();
    const maxToEpoch = Math.max(0, ...framesOf(events).map((f) => f.toEpoch));
    const gap = events.find((e) => e.kind === "gap");
    assert.ok(gap, "B2′ lower bound: a dropped/lost frame must signal a gap");
    assert.ok(
      gap.kind === "gap" && gap.head >= maxToEpoch,
      `B2′ lower bound: a gap's head must be ≥ every frame emitted before the signal (${maxToEpoch})`
    );
  } finally {
    sub.close();
  }
};

/**
 * B3 — the bus transports, it does not validate contiguity. Two non-contiguous frames
 * published with NO loss injected must BOTH be delivered faithfully (byte-identical to
 * what was published), and the bus must NOT synthesize a `gap` of its own for the
 * epoch break — contiguity validation is the kernel's job. This rejects a bus that
 * over-steps into the kernel's role (swallowing or "fixing" a non-contiguous frame).
 */
const checkTransportsWithoutValidating = async (
  make: () => BusHarness
): Promise<void> => {
  const h = make();
  const events: BusEvent[] = [];
  const sub = h.bus.subscribe((e) => {
    events.push(e);
  });
  try {
    const f1 = mkFrame(0, 1, [{ softEpoch: 1, tag: "conformance:bus:b3" }]);
    // Deliberately non-contiguous with f1 (fromEpoch 5, not 1) — yet no drop injected.
    const f2 = mkFrame(5, 6, [{ hardEpoch: 6, tag: "conformance:bus:b3" }]);
    h.publish(f1);
    h.publish(f2);
    h.pump?.();
    const frames = framesOf(events);
    assert.strictEqual(
      frames.length,
      2,
      "B3: the bus transports every published frame — it never swallows a non-contiguous one"
    );
    assert.deepStrictEqual(
      frames[0],
      f1,
      "B3: a delivered frame is faithful to what was published"
    );
    assert.deepStrictEqual(
      frames[1],
      f2,
      "B3: a non-contiguous frame is delivered as-is; the bus asserts no contiguity"
    );
    assert.ok(
      !kindsOf(events).includes("gap"),
      "B3: the bus signals a gap only for a frame IT lost, never for a contiguity break it did not cause"
    );
  } finally {
    sub.close();
  }
};

/** Key-order-independent frame equality (a wire-decoded frame is reconstructed by the reader's
 * `readFrame` with a different key order than the published literal — deep equality ignores it). */
const sameFrame = (a: BusFrame, b: BusFrame): boolean => {
  try {
    assert.deepStrictEqual(a, b);
    return true;
  } catch {
    return false;
  }
};

/**
 * ③ regId ECHO — a subscriber whose configured scope MISMATCHES the channel's must dispatch NO
 * `hello` and close LOUD (E19): the live-channel `hello` echoes the DO's `regId`, and a mismatch
 * means the subscriber read a foreign channel, so it fails closed. Requires a scope-echo control
 * (`subscribeMismatchedScope`); a bus with no scope echo (the FakeBus) self-skips to integration.
 */
const checkRegIdEcho = async (make: () => BusHarness): Promise<void> => {
  const h = make();
  if (!h.subscribeMismatchedScope) {
    return; // no scope echo (fake / redisBus w/o echo control) — deferred.
  }
  const probe = await h.subscribeMismatchedScope();
  assert.ok(
    !probe.events.some((e) => e.kind === "hello"),
    "③: a mismatched-scope subscriber must dispatch NO hello (it read a foreign channel)"
  );
  assert.ok(
    probe.closed,
    "③: a mismatched-scope subscriber must close LOUD on the regId echo mismatch (E19)"
  );
};

/**
 * ① CLOSE-ON-DOUBT — any delivery doubt (a throwing send / a closing socket) ⇒ the channel CLOSES
 * that ONE subscriber rather than risk a silent gap, and a HEALTHY co-subscriber still receives the
 * frame (one dead subscriber never blocks the fan-out). Requires a live socket to close
 * (`deliveryDoubtProbe`); a bus with none (the FakeBus, whose loss model is `drop`→`gap`) self-skips.
 */
const checkCloseOnDoubt = async (make: () => BusHarness): Promise<void> => {
  const h = make();
  if (!h.deliveryDoubtProbe) {
    return; // no live socket to make doubtful (fake) — deferred.
  }
  const frame = mkFrame(0, 1, [{ softEpoch: 1, tag: "conformance:bus:doubt" }]);
  const probe = await h.deliveryDoubtProbe(frame);
  assert.ok(
    probe.doubted.closed,
    "①: a subscriber whose delivery is doubtful must be CLOSED (never a silent gap)"
  );
  assert.ok(
    !probe.doubted.frames.some((f) => sameFrame(f, frame)),
    "①: a doubtful subscriber must NOT receive the frame — it is closed, not sent through"
  );
  assert.ok(
    !probe.healthy.closed,
    "①: one dead subscriber must not close a HEALTHY co-subscriber"
  );
  assert.ok(
    probe.healthy.frames.some((f) => sameFrame(f, frame)),
    "①: the fan-out must still reach a healthy subscriber (one dead never blocks the rest)"
  );
};

type BusCheck = (make: () => BusHarness) => Promise<void>;

/** The individually-callable Bus checks (each throws on violation; capability-gated ones self-skip). */
export const busChecks: {
  readonly closeOnDoubt: BusCheck;
  readonly gapHeadLowerBound: BusCheck;
  readonly gapOnLoss: BusCheck;
  readonly helloAfterReset: BusCheck;
  readonly helloHeadLowerBound: BusCheck;
  readonly regIdEcho: BusCheck;
  readonly resetBeforeFrame: BusCheck;
  readonly transportsWithoutValidating: BusCheck;
} = {
  closeOnDoubt: checkCloseOnDoubt,
  gapHeadLowerBound: checkGapHeadLowerBound,
  gapOnLoss: checkGapOnLoss,
  helloAfterReset: checkHelloAfterReset,
  helloHeadLowerBound: checkHelloHeadLowerBound,
  regIdEcho: checkRegIdEcho,
  resetBeforeFrame: checkResetBeforeFrame,
  transportsWithoutValidating: checkTransportsWithoutValidating,
};

export function runBusContract(
  make: () => BusHarness,
  opts: BusContractOptions = {}
): void {
  const label = opts.label ?? "Bus";
  describe(`${label} — CORE (every bus)`, () => {
    it("emits reset before any frame on every transport establishment (§4.2, E4 — B1)", async () => {
      await checkResetBeforeFrame(make);
    });
    it("emits reset → hello → frames on every transport establishment (§4.2, E21 — B1′)", async () => {
      await checkHelloAfterReset(make);
    });
    it("declares a hello head ≥ every previously-emitted frame toEpoch (§4.2, E21 — B1′ lower bound)", async () => {
      await checkHelloHeadLowerBound(make);
    });
    it("echoes the channel regId in hello; a mismatched-scope subscriber closes loud (§4.2, E19 — ③)", async () => {
      await checkRegIdEcho(make);
    });
    it("transports frames faithfully through the codec and leaves contiguity to the kernel (§4.2 — B3)", async () => {
      await checkTransportsWithoutValidating(make);
    });
    it("closes a doubtful subscriber, never a silent gap; one dead never blocks the rest (§4.2 — ①)", async () => {
      await checkCloseOnDoubt(make);
    });
  });
  describe(`${label} — CONTROLLABILITY-GATED (loss/reorder scenes)`, () => {
    it("signals a gap for a dropped/lost frame, never silently skips (§4.2 — B2′)", async () => {
      await checkGapOnLoss(make);
    });
    it("declares a gap head ≥ every frame emitted before the signal (§4.2, E21 — B2′ lower bound)", async () => {
      await checkGapHeadLowerBound(make);
    });
  });
}
