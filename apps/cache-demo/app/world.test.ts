// The demo's own state machine — arming, healing, and the rescue stopwatch.
//
// Every subject here is a place the demo could lie to your face while every gate stayed green, which
// is why they are tested away from the app that hosts them.
//
// THE TEETH IN THIS FILE STAGE COUNTERFACTUALS, and that discipline is a repair rather than a style.
// An attribution's tooth has to stage the scene where the credited mechanism is ABSENT and the
// observable is IDENTICAL — otherwise it feeds the discriminator its own inputs, pins the MAPPING
// from input to label, and certifies nothing about whether the label is TRUE. The rescue row's
// previous tooth did exactly that: it asserted that zero new dials produced `carrier`, which is the
// mapping, while the thing that needed proving was that zero new dials MEANT carrier. It did not.

import type { Explanation, TelemetryEvent } from "@astilba/cache";
import { TELEMETRY_EVENTS } from "@astilba/cache";
import { describe, expect, it } from "vitest";

import { emptyBusStats, emptyKvStats, noFaults } from "../chaos/dials";
import { SCENE_IDS } from "../chaos/script";
import type { EpisodeState, PendingPurge } from "./world";
import { armScene, healEpisode, rescueIfArrived } from "./world";

const READER: Explanation["reader"] = {
  appliedEpoch: 4,
  channel: "established",
  suspect: false,
  terminal: false,
};

/**
 * A cache stand-in. `EpisodeState` is generic over the cache for exactly this reason: the library's
 * `Cache` is a nine-method interface nothing but the real thing satisfies, so a record pinned to it
 * could only be tested by casting something into a lie. Identity is all these tests need — what is
 * on trial is whether the field is CLEARED, never what it can do.
 */
interface StandInCache {
  readonly standIn: "episode-reader";
}

/** VALUE-STABLE ON PURPOSE, and it is load-bearing for the arm→arm tooth below: that tooth compares
 * two records for structural equality, so a mint that stamped a serial number would make the two
 * differ for a reason that has nothing to do with what is being proven. A fresh object each call
 * (never a shared singleton) keeps it an honest mint. */
function mintStandIn(): StandInCache {
  return { standIn: "episode-reader" };
}

/** Everything the demo's `arm()`/`heal()` are responsible for, in one record — the same eight
 * pieces the worker holds for the life of the isolate. */
function episode(): EpisodeState<StandInCache> {
  return {
    armed: null,
    busStats: emptyBusStats(),
    dials: noFaults(),
    episodeCache: null,
    events: [] as TelemetryEvent[],
    kvStats: emptyKvStats(),
    pending: null,
    rescue: null,
  };
}

/** Dirty every piece of the record that is evidence about an armed episode. */
function dirty(state: EpisodeState<StandInCache>): void {
  state.kvStats.readsFailed += 6;
  state.kvStats.writesFailed += 2;
  state.busStats.dialsRefused += 3;
  state.events.push({ type: TELEMETRY_EVENTS.poll_tick_failed });
  state.pending = {
    appliedEpochAfterAck: 1,
    appliedEpochAtPurge: 1,
    atMs: 5,
  };
  state.rescue = {
    afterMs: 9,
    fromEpoch: 1,
    path: "own-ack",
    toEpoch: 2,
  };
}

const ARM_OPTIONS = {
  mintCache: mintStandIn,
  nowMs: 1000,
  registryName: "demo",
};

describe("heal() ends the episode — everything, not just the dials", () => {
  // THE TOOTH FOR THE DEFECT THIS REPAIRS. Healing used to clear the dials alone, so one page said
  // three contradictory things at once: that nothing was armed and the system was healthy; that six
  // KV reads had been rejected, offered as proof the fault reached the binding; and a derived
  // degraded-registry judgment. The last one LATCHED: it is derived from the event log, the log was
  // never cleared, so once any fault had ever been armed the page reported a degraded registry
  // forever.
  //
  // The invariant is the one a reader actually relies on: after arm → fault → heal, the page's state
  // EQUALS its never-armed state. Not "mostly equals" — a single surviving counter is a sentence on
  // screen claiming a fault is live.
  it.each(SCENE_IDS)(
    "arm(%s) → fault → heal leaves exactly the never-armed state",
    (sceneId) => {
      const pristine = episode();
      const state = episode();

      armScene(sceneId, state, ARM_OPTIONS);
      dirty(state);

      // NON-VACUITY FIRST. Without this the test would pass just as happily against a `heal()` that
      // did nothing at all, on a scene that armed nothing at all.
      expect(
        state,
        "the scene dirtied nothing, so healing it proves nothing"
      ).not.toStrictEqual(pristine);

      healEpisode(state);

      // THE WHOLE RECORD, in one comparison, and deliberately not field-by-field: the invariant is
      // that the healed state EQUALS the never-armed state, and a per-field list is a list someone
      // has to remember to extend. Compare the records and a field added to `episode()` next year is
      // covered the day it is added — which is exactly how the event log came to be missed, and then
      // the armed scene, the stopwatch and the rescue after it.
      expect(
        state,
        "something survived the heal — a dial, a counter, the event log that latched the judgment, or a reader booted by an episode that is over"
      ).toStrictEqual(pristine);
    }
  );

  it("clears the event log, because that is what latched the judgment", () => {
    // Called out on its own because it is the piece whose absence was invisible: a stale dial is
    // visible as a fault that will not turn off, but a stale event log looks like a healthy page
    // that has simply decided your registry is degraded.
    const state = episode();
    state.events.push({ type: TELEMETRY_EVENTS.poll_tick_failed });
    healEpisode(state);
    expect(state.events).toStrictEqual([]);
  });

  it("hands back the reader the episode booted, so the page stops talking to it", () => {
    const state = episode();
    armScene("bus-drop", state, ARM_OPTIONS);
    expect(
      state.episodeCache,
      "the scene that declares its own reader did not get one"
    ).not.toBeNull();
    healEpisode(state);
    expect(
      state.episodeCache,
      "a healed page is still holding the dead episode's reader"
    ).toBeNull();
  });
});

// ── ONE RESET PATH: arm is a heal door too ──────────────────────────────────
describe("arm() goes through the same total reset heal() does", () => {
  // THE TOOTH THE BRIEF NAMES. `armScene` called `clearFaults(dials)` and nothing else, so the
  // previous episode's counters, event log, pending purge and last rescue survived onto the new
  // scene's page and were read as evidence about ITS fault — while the page told the user, in as
  // many words, that arming this one heals that first. The aliasing lesson in miniature: one
  // function, both doors.
  //
  // MUTATION: make `armScene` skip `healEpisode` (or narrow it back to `clearFaults(state.dials)`).
  // This goes red.
  it("arm A → evidence → arm B leaves exactly what a never-armed page would show for B", () => {
    const dirtied = episode();
    armScene("backend-kill", dirtied, ARM_OPTIONS);
    dirty(dirtied);
    armScene("bus-drop", dirtied, ARM_OPTIONS);

    const fresh = episode();
    armScene("bus-drop", fresh, ARM_OPTIONS);

    // The two records must be indistinguishable: B's page after A cannot differ from B's page after
    // nothing.
    expect(
      dirtied,
      "scene B inherited scene A's episode — a counter, a log line, a stopwatch or a reader"
    ).toStrictEqual(fresh);
  });

  it("arms exactly one fault: the previous scene's dials are OFF, not merely overwritten", () => {
    const state = episode();
    armScene("backend-kill", state, ARM_OPTIONS);
    armScene("bus-drop", state, ARM_OPTIONS);
    expect(state.dials.bus.dialRefused).toBe(true);
    expect(state.dials.kv.failReads).toBe(false);
    expect(state.dials.kv.failWrites).toBe(false);
  });

  it("records what is armed and when, so no caller has to remember to", () => {
    const state = episode();
    armScene("registry-outage", state, ARM_OPTIONS);
    expect(state.armed).toStrictEqual({ atMs: 1000, id: "registry-outage" });
  });

  it("mints a reader ONLY for the scene that declares one", () => {
    // The rig is scoped. A fresh reader is the honest apparatus for the one scene whose subject is a
    // reader's first moments; everywhere else the honest thing is the reader the app actually runs.
    const deaf = episode();
    armScene("bus-drop", deaf, ARM_OPTIONS);
    expect(deaf.episodeCache).not.toBeNull();

    for (const id of ["backend-kill", "registry-outage"] as const) {
      const state = episode();
      armScene(id, state, ARM_OPTIONS);
      expect(
        state.episodeCache,
        `${id} booted a reader it never declared — the page would be narrating a newborn and calling it the app's`
      ).toBeNull();
    }
  });

  it("mints the reader AFTER the dial is set — the whole point of the rig", () => {
    // If the mint ran before the fault was armed, the reader's first use could land on a healthy
    // dial and the scene would show `established` — the exact state it exists to disprove.
    const state = episode();
    let dialAtMint: boolean | null = null;
    armScene("bus-drop", state, {
      ...ARM_OPTIONS,
      mintCache: () => {
        dialAtMint = state.dials.bus.dialRefused;
        return mintStandIn();
      },
    });
    expect(
      dialAtMint,
      "the episode's reader was minted before its fault was armed, so its first dial could succeed"
    ).toBe(true);
  });
});

/** A witness reporting an advanced epoch on a channel in `channel`. */
function advanced(channel: Explanation["reader"]["channel"]) {
  return {
    explanation: {
      key: "k",
      kind: "absent",
      reader: { ...READER, appliedEpoch: 5, channel },
      tier: "miss",
    },
    ok: true,
  } as const;
}

describe("the poll-rescue stopwatch", () => {
  const pending: PendingPurge = {
    appliedEpochAfterAck: 4,
    appliedEpochAtPurge: 4,
    atMs: 1000,
  };

  it("declares no rescue while the reader's epoch has not moved", () => {
    // The epoch is the entire proof. A reader that was ASKED to learn something and did not has not
    // been rescued, however long the stopwatch has run.
    expect(
      rescueIfArrived(
        pending,
        {
          explanation: {
            key: "k",
            kind: "absent",
            reader: READER,
            tier: "miss",
          },
          ok: true,
        },
        9999
      )
    ).toBeNull();
  });

  // ── THE COUNTERFACTUAL PAIR ───────────────────────────────────────────────
  //
  // These two stage the SAME observable — a purge issued, the applied epoch advancing from 4 to 5,
  // no own-ack — and differ in exactly one thing: whether the reader had a channel. That is the
  // discriminator, so that is what the pair varies. A dial-count delta detects only a socket that
  // came UP during the wait; a socket that opened before the purge and stayed up produces no new
  // dial, so its delta is zero and it would print `carrier` over a live bus. The first test below is
  // the scene where the credited mechanism is ABSENT and the observable is identical — the dial-delta
  // discriminator prints `carrier` for it, which is the mutation that test kills.

  it("REFUSES to prove the carrier when the channel was UP — the unsupported carrier credit", () => {
    // MUTATION: restore the dial-delta discriminator (`dialsPassedNow === pending.dialsPassedAtPurge
    // ? "carrier" : "bus-open"`). An established bus that dialled nothing new during the wait has a
    // zero delta, so it prints `carrier` — "proven" — on a fully live socket. This goes red.
    const rescue = rescueIfArrived(pending, advanced("established"), 8300);
    expect(
      rescue?.path,
      "the demo proved a carrier over a bus that was up the whole time"
    ).toBe("indeterminate");
  });

  it("proves the carrier when the reader had NO channel — the bus is eliminated, not argued away", () => {
    for (const channel of ["never-established", "lost"] as const) {
      const rescue = rescueIfArrived(pending, advanced(channel), 8300);
      expect(
        rescue?.path,
        `a reader whose channel is ${channel} has nowhere for a frame to have arrived, so the poll is the only path left`
      ).toBe("carrier-proven");
    }
  });

  it("gives the two channel worlds two DIFFERENT verdicts", () => {
    // Non-vacuity for the pair above: either case alone could be satisfied by a constant. Only their
    // disagreement proves the branch is live and reading the channel at all.
    expect(
      rescueIfArrived(pending, advanced("established"), 8300)?.path
    ).not.toBe(
      rescueIfArrived(pending, advanced("never-established"), 8300)?.path
    );
  });

  it("reports the epochs and the elapsed time it measured", () => {
    expect(
      rescueIfArrived(pending, advanced("never-established"), 8300)
    ).toStrictEqual({
      afterMs: 7300,
      fromEpoch: 4,
      path: "carrier-proven",
      toEpoch: 5,
    });
  });

  it("names the OWN-ACK path when expire()'s ack taught this isolate's own reader", () => {
    // The unsupported carrier credit, pinned in a test. The reader's epoch had already moved by the
    // time the ack returned — so no tick ran and no frame arrived, and the poll carrier did nothing.
    // The channel was dead, which is exactly what a genuine carrier rescue looks like, so the channel
    // cannot tell them apart either. Only the after-ack epoch can, which is why own-ack is asked
    // first.
    const rescue = rescueIfArrived(
      { ...pending, appliedEpochAfterAck: 5 },
      advanced("never-established"),
      1002
    );
    expect(rescue?.path, "an own-ack was credited to the carrier").toBe(
      "own-ack"
    );
  });

  it("declares no rescue with nothing pending, and none when the witness could not report", () => {
    expect(rescueIfArrived(null, advanced("established"), 8300)).toBeNull();
    expect(
      rescueIfArrived(pending, { error: "boom", ok: false }, 8300)
    ).toBeNull();
  });
});
