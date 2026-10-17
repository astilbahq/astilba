// Ordinary unit teeth (NOT sacred). The telemetry event-name CATALOG + the
// STRUCTURAL type tooth on `safeEmit`. Each case bites one guarantee:
//   • structural tooth — an OFF-CATALOG event name at a `safeEmit` call is a COMPILE error, not a
//     grep finding. Blind the narrowing (`TelemetryEvent.type: string`) and the `@ts-expect-error`
//     below goes unused ⇒ `tsc --noEmit` reds. (Checked by the required typecheck gate — test/** is
//     in the kernel tsconfig; vitest's esbuild transform ignores the directive, so this file also
//     RUNS clean.)
//   • exhaustiveness tooth — every catalog member carries an emitting-layer annotation. Enroll a new
//     name in `TELEMETRY_EVENTS` without annotating it here and the `satisfies` fails to compile.
// • roster — the catalog's value equals its key (name-as-value) and enrolls exactly the set.

import { describe, expect, it } from "vitest";

import { safeEmit, TELEMETRY_EVENTS } from "../../src/telemetry";
import type { TelemetryEvent, TelemetryEventName } from "../../src/telemetry";

/** The emitting layer of each catalog event. Every name carries one, and this table is the half a
 * machine can check. `satisfies Record<TelemetryEventName, TelemetryLayer>` is the
 * EXHAUSTIVENESS tooth: a catalog member with no annotation here fails to compile, and an
 * annotation for a non-member is an excess-property error. (Layer taxonomy lives in the test, so no
 * otherwise-unused production export exists.) */
type TelemetryLayer = "kernel" | "l1-driver" | "react-router" | "cloudflare";

const EMITTING_LAYER = {
  writeback_throttled: "kernel",
  l2_write_error: "kernel",
  singleflight_option_mismatch: "kernel",
  store_read_suppressed: "kernel",
  // the write-layer guard lives in the kernel's write-back (`bornMonotoneWrite`), so the event is
  // the kernel's: no driver can tell a refused write from a write nobody asked for.
  neg_suppressed: "kernel",
  private_evicted: "l1-driver",
  poll_tick_failed: "react-router",
  l3_ineligible: "react-router",
  regid_divergence: "cloudflare",
  // Raw facts. `bus_dial_failed` is emitted by the KERNEL reader and ONLY there: it is the thing
  // that tracks channel state, receives the `lost` declaration, and therefore is the only one that
  // can tell a dial that never came up from a healthy socket dropping. `strong_degraded` is the
  // kernel read path (the opt-out is a kernel-level posture).
  bus_dial_failed: "kernel",
  strong_degraded: "kernel",
  // RESERVED (degraded-mode names, emit condition unruled — FOR FABLE). Provisionally annotated
  // react-router, since `poll_tick_failed` (their eventual home per the note) emits there.
  registry_degraded: "react-router",
  state_stale: "react-router",
} satisfies Record<TelemetryEventName, TelemetryLayer>;

describe("telemetry event-name catalog (CACHE-21)", () => {
  it("STRUCTURAL TOOTH: an off-catalog event name is a compile error at a safeEmit call", () => {
    const delivered: string[] = [];
    const sink = (event: TelemetryEvent): void => {
      delivered.push(event.type);
    };

    // Positive control (non-vacuity): a CATALOG member is accepted with no directive.
    safeEmit(sink, { type: TELEMETRY_EVENTS.private_evicted });

    // @ts-expect-error D2 structural tooth — "not_a_real_event" is not a TelemetryEventName, so a
    // safeEmit call carrying it is a COMPILE error (the type IS the guard, not a grep). Blinding the
    // narrowing (`TelemetryEvent.type: string`) makes this line compile ⇒ the directive is unused ⇒
    // the required `tsc --noEmit` gate reds. safeEmit does not validate names at RUNTIME, so both
    // calls deliver — the tooth is entirely at compile time.
    safeEmit(sink, { type: "not_a_real_event" });

    expect(delivered).toStrictEqual(["private_evicted", "not_a_real_event"]);
  });

  it("EXHAUSTIVENESS TOOTH: every catalog member carries an emitting-layer annotation", () => {
    // The `satisfies` above is the compile-time half; this is the runtime non-vacuity that the map
    // covers the catalog exactly (a missing member would already have failed to compile).
    expect(Object.keys(EMITTING_LAYER).toSorted()).toStrictEqual(
      Object.values(TELEMETRY_EVENTS).toSorted()
    );
  });

  it("the catalog value equals its key for every member (name-as-value)", () => {
    for (const [key, value] of Object.entries(TELEMETRY_EVENTS)) {
      expect(value).toBe(key);
    }
  });

  it("enrolls exactly the roster of event names (C5-a + the E28 raw facts + E7's refused write)", () => {
    expect(new Set(Object.values(TELEMETRY_EVENTS))).toStrictEqual(
      new Set([
        "writeback_throttled",
        "l2_write_error",
        "singleflight_option_mismatch",
        "store_read_suppressed",
        "neg_suppressed",
        "private_evicted",
        "poll_tick_failed",
        "l3_ineligible",
        "regid_divergence",
        "bus_dial_failed",
        "strong_degraded",
        "registry_degraded",
        "state_stale",
      ])
    );
  });

  it("the E28 additions are LIVE raw facts, and the reserved judgments stay reserved", () => {
    // The raw-fact/derived-judgment line, held as a roster rather than a convention: a
    // raw fact states what happened at one instant and may emit the moment it happens; a judgment
    // states a verdict over accumulated state and has no legitimate emit point until its threshold
    // is ruled. `bus_dial_failed` and `strong_degraded` are facts — this dial did not come up, this
    // call degraded — so they emit. `registry_degraded`/`state_stale` remain judgments with no ruled
    // threshold, so they stay enrolled-but-silent. Moving either across this line is a ruling, not a
    // refactor, and this case is where that shows up.
    expect(TELEMETRY_EVENTS.bus_dial_failed).toBe("bus_dial_failed");
    expect(TELEMETRY_EVENTS.strong_degraded).toBe("strong_degraded");
    expect(TELEMETRY_EVENTS.registry_degraded).toBe("registry_degraded");
    expect(TELEMETRY_EVENTS.state_stale).toBe("state_stale");
  });
});
