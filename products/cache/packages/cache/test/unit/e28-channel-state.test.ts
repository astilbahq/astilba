// Ordinary unit teeth (NOT sacred). Channel state, the declared-degraded mode, and the
// `explain()` surface over it. Each case bites ONE branch:
//
//   1. NEVER-ESTABLISHED IS DECLARED, NOT SILENT — a reader whose bus never came up reports
//      `never-established`, and a failed dial is DECLARED (`lost` ⇒ `bus_dial_failed`) rather than
//      going quiet. THE MUTATION: dial-failure-as-silence (drop the `lost` declaration) ⇒ the reader
//      reads exactly like a healthy-but-quiet one ⇒ red.
// 2. NEVER-ESTABLISHED ≠ SUSPICION — the declared mode does NOT raise the suspect flag.
//      Blind it into suspicion and every cold boot reads as a gapped map ⇒ red.
//   3. ESTABLISHED ON HELLO — and only on hello: a socket that resolved but never spoke has not
//      established anything.
//   4. LOST ONLY AFTER ESTABLISHMENT — a channel that never came up did not LOSE anything; it never
//      had it. Blind the distinction and the two states collapse ⇒ red.
//   5. explain() SURFACES IT, and adds no bookkeeping — the witness may speak, it may never touch
// cache state: explain does NOT dial.
//
// Lane: unit. Deterministic — a hand-driven bus, no timers, no wall clock.

import { memory } from "@astilba/cache";
import type {
  Bus,
  BusEvent,
  Subscription,
  TelemetryEvent,
} from "@astilba/cache";
import { FakeRegistry, SimClock } from "@astilba/harness";
import { describe, expect, it } from "vitest";

import { createCache } from "../../src/cache";
import { Invalidation } from "../../src/invalidation";

/** A bus the test drives by hand: it records whether anyone subscribed, and lets the case deliver the
 * exact BusEvent sequence a real transport would produce.
 *
 * `deliver`, NOT `push`, and the name is load-bearing rather than taste. This method hands ONE frame
 * to the subscriber; it is not an array append, and it takes exactly one argument. Named `push`, it
 * drew `unicorn/prefer-single-call` — a rule about `Array#push` — onto every pair of consecutive
 * frame deliveries, and that rule's autofix rewrites `bus.push(a); bus.push(b);` into
 * `bus.push(a, b)`: one frame delivered, the second silently dropped, the channel never established.
 * The tooth that turns on the `hello` would have been asserting against a reader that never got one.
 * (Caught by tsc — TS2554 — rather than by anything reading the diff, which is the part worth
 * remembering.) A test double that is not an array should not be named as if it were one; renaming
 * it removes the false positive at its source instead of leaving a disable comment to be obeyed. */
class HandBus implements Bus {
  subscribes = 0;
  #onEvent: ((event: BusEvent) => void) | null = null;

  subscribe(onEvent: (event: BusEvent) => void): Subscription {
    this.subscribes += 1;
    this.#onEvent = onEvent;
    return {
      close: (): void => {
        this.#onEvent = null;
      },
    };
  }

  deliver(event: BusEvent): void {
    this.#onEvent?.(event);
  }
}

/** Drive the reader to an ESTABLISHED channel: the opening two frames a real establishment sends, in
 * the order it sends them — the episode marker first, then the far end's answer. */
function establish(bus: HandBus, head = 5): void {
  bus.deliver({ kind: "reset" });
  bus.deliver({ head, kind: "hello" });
}

function reader(): {
  inval: Invalidation;
  bus: HandBus;
  events: TelemetryEvent[];
} {
  const clock = new SimClock(0);
  const bus = new HandBus();
  const events: TelemetryEvent[] = [];
  const inval = new Invalidation(
    new FakeRegistry(clock, {}),
    memory(),
    bus,
    undefined,
    (event) => {
      events.push(event);
    }
  );
  inval.ensureSubscribed();
  return { bus, events, inval };
}

describe("E28 — the reader reports its channel rather than inferring it from quiet", () => {
  it("a reader that has never heard a hello reports never-established", () => {
    const { inval } = reader();
    // The honest resting state. A cache that has just been built has not connected to anything, and
    // it says so rather than resting on the absence of bad news.
    expect(inval.channel).toBe("never-established");
  });

  it("a FAILED DIAL is DECLARED: `lost` ⇒ bus_dial_failed, and the channel stays never-established", () => {
    const { inval, bus, events } = reader();
    // The transport reports its dial failure as a `lost` declaration.
    bus.deliver({ kind: "lost" });

    // THE MUTATION — dial-failure-as-silence: drop the `lost` declaration (the earlier shape, where
    // the rejection was swallowed inside a detached IIFE) and BOTH assertions red. That is the exact
    // failure this erratum exists to end: with nothing on the wire, a bus that never connected and a
    // healthy bus with nothing to say produce identical evidence — none — so the reader would report
    // a channel it does not have.
    expect(events.filter((e) => e.type === "bus_dial_failed")).toHaveLength(1);
    // It did not LOSE a channel it never had (case 4): the state is still never-established.
    expect(inval.channel).toBe("never-established");
  });

  it("a HEALTHY CLOSE is NOT a dial failure: an established socket dropping emits ZERO", () => {
    const { inval, bus, events } = reader();
    // A working channel: the far end echoed our scope and declared its head.
    establish(bus);
    expect(inval.channel).toBe("established");

    // …and then the socket ends — an eviction, a deploy, a server close. The transport reports the
    // SAME `lost` it reports for a dial that never came up, because from the socket's side both are
    // just an ending. Only the reader knows a `hello` arrived on this one.
    bus.deliver({ kind: "lost" });

    // THE TOOTH. Blind the healthy-close gate (emit on every `lost`) and this reds at 1 — every
    // eviction on a working fleet would be counted as a dial failure, which is how a real signal
    // becomes noise precisely when someone is relying on it.
    expect(events.filter((e) => e.type === "bus_dial_failed")).toHaveLength(0);
    expect(inval.channel).toBe("lost");
  });

  it("a failed dial emits EXACTLY ONE — including a redial after a healthy channel dropped", () => {
    const { bus, events } = reader();
    establish(bus);
    bus.deliver({ kind: "lost" }); // the healthy close: not counted.
    expect(events.filter((e) => e.type === "bus_dial_failed")).toHaveLength(0);

    // The redial fails: no reset, no hello, just an ending. THIS is a dial failure — the channel is
    // down and the attempt to bring it back did not come up.
    bus.deliver({ kind: "lost" });

    // EXACTLY ONE, which is the count the per-failed-dial wording names. It read as two while the
    // adapter wired the same counter to the same sink from its own end; a counter that double-counts
    // is not a counter. Over-gate this (skip the emit whenever the channel is not
    // never-established) and it reds at 0 — a fleet whose bus dies and cannot come back would go
    // silent at exactly the moment it should be loudest.
    expect(events.filter((e) => e.type === "bus_dial_failed")).toHaveLength(1);
  });

  it("never-established is DECLARED DEGRADED, not SUSPICION (E20/E21 keep their posture)", () => {
    const { inval, bus } = reader();
    bus.deliver({ kind: "lost" });
    // the suspect flag is for a map that knows it MISSED something. A reader that never
    // connected has missed nothing — it has no channel at all — and those are different states with
    // different remedies. Blind this
    // into suspicion and every cold boot reads as a gapped map, which would make the suspect flag
    // mean nothing at exactly the moment it needs to mean something.
    expect(inval.suspect).toBeFalsy();
  });

  it("ESTABLISHED on hello — and lost only AFTER establishment (the two states never collapse)", () => {
    const { inval, bus } = reader();
    bus.deliver({ kind: "reset" });
    // A reset alone does not establish: the far end has not spoken yet.
    expect(inval.channel).toBe("never-established");

    bus.deliver({ head: 0, kind: "hello" });
    // `hello` is the DO echoing our scope and declaring its head — no half-open socket produces it.
    expect(inval.channel).toBe("established");

    bus.deliver({ kind: "lost" });
    // NOW a loss is a loss: this reader had a channel and no longer does. Blind the distinction
    // (treat every `lost` as never-established, or every one as lost) and one of this case's two
    // halves reds — which is the point: the states carry different histories.
    expect(inval.channel).toBe("lost");
  });
});

describe("E28 — explain() surfaces the channel (E27: the witness may speak, never touch)", () => {
  it("reports never-established on a cache whose bus never came up", async () => {
    const clock = new SimClock(0);
    const bus = new HandBus();
    const cache = createCache({
      bus,
      clock,
      l1: memory(),
      l2: memory(),
      namespace: "e28-explain",
      registry: new FakeRegistry(clock, {}),
      rng: { next: () => 0.5 },
    });

    const explanation = await cache.explain("nothing");
    // The field that tells a dead bus from a quiet one. `suspect` cannot: it is FALSE here, and it
    // is false for a perfectly healthy reader too — which is exactly why reading health off it alone
    // is the lie this field exists to prevent.
    expect(explanation.reader.channel).toBe("never-established");
    expect(explanation.reader.suspect).toBeFalsy();
  });

  it("explain() does NOT dial — introspection adds no I/O (E27, E28)", async () => {
    const clock = new SimClock(0);
    const bus = new HandBus();
    const cache = createCache({
      bus,
      clock,
      l1: memory(),
      l2: memory(),
      namespace: "e28-explain",
      registry: new FakeRegistry(clock, {}),
      rng: { next: () => 0.5 },
    });

    await cache.explain("nothing");
    // THE TOOTH: wire `ensureSubscribed()` into the explain path and this reds at 1. A witness that
    // dialed would be testimony rather than introspection — it would CREATE the channel it was asked
    // to report on, and the report would be of its own making.
    expect(bus.subscribes).toBe(0);

    // …and a READ does subscribe, so the zero above is deferral, not a dead bus (non-vacuity).
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
    });
    expect(bus.subscribes).toBe(1);
  });
});
