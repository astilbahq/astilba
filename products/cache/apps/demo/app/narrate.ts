// The narration — measured behaviour beside documented bounds, and nothing else.
//
// This is the demo's whole argument, so it is worth being precise about what it is allowed to say.
// `explain()` is a witness: it reports what the system already knows at the instant of the call and
// reports what it cannot know as `unknown`. This module turns that testimony into rows,
// pairs each row with the bound this deployment documents for it, and stops. It draws no
// conclusion. It has no pass/fail. It never says a bound was met.
//
// That restraint is the product's argument rather than a hedge around it. The interesting claim
// this library makes is not that it is fast — it is that when it does not know whether an entry is
// still true, it says so and pays for an answer instead of guessing. A narration that graded itself
// against an SLO would be advertising the least interesting property it has, and would go red on a
// slow afternoon for reasons that have nothing to do with correctness. So: what happened, what was
// permitted, side by side, including the times the answer is unflattering.
//
// PURE, and taking a plain record rather than the world: the narration is the part of this app most
// worth testing, and a function that reaches for a cache is a function that needs one to test it.

import type {
  ExplainedDependency,
  Explanation,
  TelemetryEvent,
  TelemetryEventName,
} from "@astilba/cache";
import { TELEMETRY_EVENTS } from "@astilba/cache";

import type { BusFaultStats, FaultDials, KvFaultStats } from "../chaos/dials";
import type { Scene } from "../chaos/script";
import { LIFETIME_CAVEAT, ms, NO_SLO_CAVEAT, POSTURE } from "./bounds";
import type { WitnessResult } from "./probe";
import type { MeasuredRescue, RescuePath } from "./world";

/** One line of the narration: what the system did, and what it was permitted to do. */
interface NarrationRow {
  readonly label: string;
  /** What this run actually observed. */
  readonly measured: string;
  /** What the documentation permits. Never a target, never a threshold that was "met". */
  readonly documented: string;
}

/** A judgment the demo DERIVED, with the threshold it used — see `deriveJudgments`. */
interface DerivedJudgment {
  readonly event: TelemetryEventName;
  /** The demo's own threshold, stated so a reader can disagree with it. */
  readonly because: string;
}

/** What the console prints for one run. */
export interface Narration {
  readonly rows: readonly NarrationRow[];
  readonly judgments: readonly DerivedJudgment[];
  readonly caveat: string;
  /** How long this page's reader lives, printed under every table — see `LIFETIME_CAVEAT`. */
  readonly lifetimeCaveat: string;
}

/** Everything the narration needs, and nothing that could let it cheat. */
export interface NarrationInput {
  /** What the witness reported — or the error it raised trying to look. */
  readonly witness: WitnessResult;
  /** The armed scene, or `undefined` for a healthy run. */
  readonly scene: Scene | null;
  /** How long the current fault has been armed — the demo's proxy for the silence the reader has
   * had a chance to observe. `undefined` when nothing is armed. */
  readonly armedForMs: number | null;
  readonly dials: FaultDials;
  readonly kvStats: KvFaultStats;
  readonly busStats: BusFaultStats;
  /** The coordinator end of the heartbeat number, as `env` actually carried it. */
  readonly coordinatorHeartbeatMs: number | null;
  readonly events: readonly TelemetryEvent[];
  /** What the read cost, measured at the app's edge — `undefined` when no read was timed. */
  readonly readMs: number | null;
  /** Did the factory run this request? The cheapest proof a read was a FILL rather than a serve,
   * and one the cache cannot fake for us. */
  readonly filled: boolean;
  /** A purge that arrived, and how long the poll carrier took to bring it. */
  readonly rescue: MeasuredRescue | null;
  /** A purge issued and still unarrived — the stopwatch is running. */
  readonly awaitingRescueForMs: number | null;
}

/** The `Explanation` union's `present` arm, or `undefined`. The other two arms are REPORTABLE
 * states, never errors — `absent` is the store answering "nothing", `read-failed` is the store
 * not answering — so both narrate rather than throw. */
function presentArm(
  explanation: Explanation
): Extract<Explanation, { kind: "present" }> | undefined {
  return explanation.kind === "present" ? explanation : undefined;
}

/** How the stored ttl reads. The witness reports `not-stored` where the format carries no ttl, and
 * that is a real answer rather than a gap: reporting a reconstruction's default as though the store
 * had carried it would be the witness testifying to bytes that were never written. */
function ttlLine(explanation: Explanation): string {
  const present = presentArm(explanation);
  if (present === undefined) {
    return "—";
  }
  const { ttl } = present.identity;
  return ttl.kind === "stored" ? `${ttl.seconds}s (stored)` : "not stored";
}

/**
 * How the stored scope reads, preserving the readable-vs-unreadable distinction.
 *
 * THE DISTINCTION IS THE ROW. A scope that is readable and exactly `pub` is the only thing that
 * makes a response shared-cacheable; absent, malformed, or unreadable is non-public and poisons it.
 * Those two outcomes are one ternary apart in this function and worlds apart on the wire, which is
 * why the tooth beside this file drives BOTH arms and asserts they disagree — an inverted branch
 * here would report a per-principal entry as public, and every gate in this repo would stay green
 * while it did.
 */
function scopeLine(explanation: Explanation): string {
  const present = presentArm(explanation);
  if (present === undefined) {
    return "—";
  }
  const { scope } = present.identity;
  return scope.kind === "readable"
    ? scope.literal
    : "unreadable (fails closed)";
}

/** The verdict and the evidence behind it — the epochs the born epoch was weighed against. */
function verdictLine(explanation: Explanation): string {
  const present = presentArm(explanation);
  if (present === undefined) {
    return "—";
  }
  const { validity, softEpoch, hardEpoch } = present.verdict;
  return `${validity} — weighed against soft ${softEpoch} / hard ${hardEpoch}, born ${present.identity.bornEpoch}`;
}

/**
 * The two derived telemetry names, emitted by the DEMO because the demo is their first acting
 * consumer.
 *
 * The catalog separates raw facts from judgments, and the separation is load-bearing.
 * `poll_tick_failed` and `bus_dial_failed` are facts: something failed, and the instant it fails
 * there is something true to report. `registry_degraded` and `state_stale` are judgments — they are
 * thresholds over accumulated state, and a threshold has no legitimate emit point until someone has
 * decided what it is AND is going to act on it. Nobody had, so the library enrolled the names and
 * emits neither.
 *
 * The demo is the first thing that ever needed to act on them, so the thresholds are HERE, in the
 * consumer, where they can be argued with — and they are the demo's, not the library's. They are
 * also uncalibrated: nothing has yet measured how long a healthy deployment's silences actually
 * run, so these are the reasonable-looking numbers of someone who has not yet looked. Deployed
 * probes are what would turn them into numbers worth putting in a library, and until then they stay
 * out here.
 *
 * A THRESHOLD IS OVER THE SYSTEM'S EVIDENCE, NEVER OVER THE APPARATUS'S CLOCK — and `state_stale`
 * is here because it once was not. It fired on how long ago someone had pressed the Arm button, and
 * then stated as fact that "the mirror pointer has gone unread", while every mirror read on the page
 * was succeeding. A stopwatch on a button cannot establish anything about a pointer:
 * "time since I pressed Arm" is a fact about the demo's own furniture. It now rests on the reader's own
 * flush-marker-anchored silence measurement, surfaced as `suspect` — the system measuring itself,
 * which the demo may read but may not impersonate.
 *
 * WHAT IS STILL MISSING, stated because the absence is the finding rather than an embarrassment: the
 * reader measures that silence in MILLISECONDS and the witness does not carry the number, only the
 * raise. So the demo can say THAT the pointer went unread past the threshold and cannot say for how
 * long — and it declines to fill that gap with a number it owns. A witness adds no bookkeeping to
 * answer a question, so the missing number is a real design question, not an oversight to
 * patch from out here.
 */
/** Which of the reader's degradation facts to name first — the most specific one that is true. */
function degradedBecause(reader: {
  suspect: boolean;
  terminal: boolean;
}): string {
  if (reader.terminal) {
    return "terminal";
  }
  return reader.suspect ? "suspect" : "polling into failures";
}

function deriveJudgments(input: NarrationInput): readonly DerivedJudgment[] {
  const judgments: DerivedJudgment[] = [];
  const reader = input.witness.ok
    ? input.witness.explanation.reader
    : undefined;
  const ticksFailed = input.events.filter(
    (event) => event.type === TELEMETRY_EVENTS.poll_tick_failed
  ).length;
  if (reader === undefined) {
    // The witness RAISED, so there is no reader state to judge over. That is itself worth reporting
    // as degradation — but it is reported as what it is (the witness raised), not as a reader state
    // the demo did not actually observe.
    judgments.push({
      because:
        "the witness raised instead of reporting, so the reader's own state was never observed — degraded is what the demo can honestly say; anything more would be invented",
      event: TELEMETRY_EVENTS.registry_degraded,
    });
  } else if (reader.suspect || reader.terminal || ticksFailed > 0) {
    judgments.push({
      because: `the reader is ${degradedBecause(reader)} (${ticksFailed} swallowed tick${ticksFailed === 1 ? "" : "s"}) — the demo's threshold, not the library's`,
      event: TELEMETRY_EVENTS.registry_degraded,
    });
  }
  if (reader?.suspect === true) {
    judgments.push({
      because: `the reader's OWN silence measurement raised suspicion — it watches the mirror pointer's flush marker fail to advance and raises once that silence reaches ${ms(POSTURE.suspicionAfterMs)} (E5). The demo takes that raise as its threshold and adds no clock of its own: the reader measured the pointer, the demo only read the answer — the demo's threshold, not the library's`,
      event: TELEMETRY_EVENTS.state_stale,
    });
  }
  return judgments;
}

/** `3 reads` / `1 read` — the counters read as sentences, and a stray plural reads as sloppiness on
 * a page whose whole argument is precision. */
function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

/**
 * THE CAMERA NOTE WITH NOTHING ARMED — and it names its baseline, because the page falsifies the
 * alternative before it renders.
 *
 * It used to say "every dial is off and every counter below reads zero". The second half was
 * STRUCTURALLY IMPOSSIBLE on the page carrying it: this route's own loader reads the probe and
 * witnesses it, both of which go through the wrapper, both of which increment the PASS counters —
 * before a byte of this HTML exists. So the note asserted a state its own page had already made
 * false, and anyone who looked down two inches could see it.
 *
 * The counters are not wrong and the note was. What reads zero on a healthy page is the FAULT
 * counters; the pass counters read this page's own traffic, which is exactly what they are for. An
 * observation names its apparatus, and the apparatus here includes the observer.
 *
 * It lives beside the narration rather than in the route because a sentence making a claim about
 * numbers is a sentence worth testing, and the route module cannot be loaded by the demo's lane.
 */
export const HEALTHY_NOTE =
  "Not armed: every dial is off, and every FAULT counter below reads zero — healing ends the episode those counters were evidence about. The pass counters are NOT zero, and should not be: this page's own loader read and witnessed the entry through the same wrapper before this HTML rendered, so what they count is the demo's own traffic. That is the baseline; anything above it is the fault. Arm, then reload to watch.";

/** Did the armed fault actually intercept anything? A scene that faulted nothing looks, on screen,
 * exactly like a system that shrugged it off — so the counters are printed, always, and a scene
 * that reads "0 intercepted" is telling you the demo is broken rather than that the cache is
 * strong. */
function interceptionRow(input: NarrationInput): NarrationRow {
  const { kvStats, busStats } = input;
  return {
    documented:
      "the demo's own wrappers — proof the fault reached the binding rather than being narrated into existence. A scene reading 0 rejected has not survived anything; it has failed to happen. Healing zeroes these: they are evidence about the armed episode, and the episode is over.",
    label: "faults intercepted",
    measured: `KV: ${plural(kvStats.readsFailed, "read")} / ${plural(kvStats.writesFailed, "write")} rejected, ${plural(kvStats.readsPassed, "read")} / ${plural(kvStats.writesPassed, "write")} passed · dials: ${busStats.dialsRefused} refused, ${busStats.dialsPassed} passed`,
  };
}

/** Both ends of the one heartbeat number, and whether they agree. */
function heartbeatRow(input: NarrationInput): NarrationRow {
  const coordinator = input.coordinatorHeartbeatMs;
  let measured: string;
  if (coordinator === null) {
    measured = `reader ${ms(POSTURE.heartbeatIntervalMs)} · coordinator UNSET (dormant — no idle heartbeat, by default and on purpose)`;
  } else {
    const verdict =
      coordinator === POSTURE.heartbeatIntervalMs
        ? "they agree, because this deployment set them both"
        : "THEY DISAGREE";
    measured = `reader ${ms(POSTURE.heartbeatIntervalMs)} · coordinator ${ms(coordinator)} — ${verdict}`;
  }
  return {
    documented:
      "one number, two deployment surfaces, nothing enforcing that they match — a Worker cannot write a Durable Object's env (R6). A shorter reader interval errs toward suspicion: the priced direction.",
    label: "heartbeat, both ends",
    measured,
  };
}

/** What the demo's sink actually received. */
function eventsRow(input: NarrationInput): NarrationRow {
  return {
    documented:
      "what a hero-path app can see: the factory takes a telemetry sink, so the kernel's own raw facts arrive here — `bus_dial_failed` and `strong_degraded` among them. The two DERIVED names are not in this list and never will be: the library enrols them and emits neither, because a judgment needs a threshold nobody has measured yet. The demo derives those itself, below.",
    label: "telemetry events seen",
    measured:
      input.events.length === 0
        ? "none"
        : input.events.map((event) => event.type).join(", "),
  };
}

/**
 * THE CHANNEL ROW — the one row that tells a dead bus from a quiet healthy one.
 *
 * This row exists because `suspect` cannot answer the question an operator actually asks. A reader
 * whose bus never came up is not suspect: it has no gap, because it has no channel — so a dead bus
 * and a serene one produce the identical `suspect: false`. That was the lie this whole arc exists
 * to end, and this is where the page tells the truth instead: a channel that never established is a
 * DECLARED state, and the reader declares it.
 */
function channelRow(explanation: Explanation): NarrationRow {
  const { channel } = explanation.reader;
  let measured: string;
  if (channel === "never-established") {
    measured =
      "NEVER-ESTABLISHED — no hello has ever arrived on this reader's channel";
  } else if (channel === "lost") {
    measured = "LOST — the channel came up once and is now down";
  } else {
    measured = "established";
  }
  return {
    documented:
      "a channel that never established is a declared state, never silence (E28). `established` is recorded by a `hello` and by nothing else — a socket that opened but was never answered has established nothing. While the channel is down, correctness rests on the poll carrier and the fail-closed unknown posture, not on the bus.",
    label: "reader channel",
    measured,
  };
}

/** What each path means on screen — one sentence each, and the two unflattering ones are the longest
 * because they are the ones this page actually produces. */
const PATH_NOTE: Readonly<Record<RescuePath, string>> = {
  "carrier-proven":
    "the reader had NO CHANNEL when the cursor moved, so no frame can have arrived on a socket that was not there — the poll of the mirror pointer is the only path left that moves this cursor. The bus is not argued away here; it is ruled out by a state the reader declares (E28)",
  indeterminate:
    "the channel was UP when the cursor moved, so either the socket or the poll could have carried it — this page cannot tell which, and does not claim to. Naming the more interesting path on this evidence would be a guess wearing a measurement's clothes",
  "own-ack":
    "NOT A RESCUE. This isolate issued the purge, and `expire()`'s own registry ack taught its reader on the spot — before a poll tick was due, with every dial refused. The reader was never lost, so nothing carried anything. A single-isolate page can only ever produce this: the purger and the reader are the same object.",
};

/**
 * WHY THE CURSOR HAS NOT MOVED YET — read off the reader's channel, never assumed.
 *
 * This arm used to hard-code a dead bus — announcing that the cursor may never move and that the
 * socket is not there — printed verbatim on whatever system happened to be underneath. On a healthy page it
 * fired anyway — beside a channel row reading `established`, in the same table, contradicting it.
 * The fix is the same one the path discriminator got: read the state, say what it shows.
 */
function waitingNote(witness: WitnessResult): string {
  if (!witness.ok) {
    return "The witness raised, so this page has no reader state to explain the wait with — what it will not do is invent one.";
  }
  const { channel } = witness.explanation.reader;
  if (channel === "established") {
    return "The channel is UP, so a frame has somewhere to arrive: this may simply be a wait. Nothing here says the bus is broken, and this page will not say it because the number is still zero.";
  }
  return "The reader has no channel, so it may never move: read the verdict row, where the reader can still learn the purge by failing closed to a live registry check. That is convergence; this is a socket that is not there.";
}

/**
 * THE BUS-FRAME CURSOR after a purge — and this row is a lesson in naming the number you watch.
 *
 * IT WATCHES `reader.appliedEpoch`, WHICH IS NOT "HAS THE READER LEARNED THE PURGE". It is the
 * highest CONTIGUOUSLY applied bus-frame epoch: a cursor over the live channel. With the bus dead it
 * may never move at all, and that is not a reader failing to converge — the verdict row above is
 * where convergence actually shows, and under a dead bus this page shows the entry going `stale`
 * off a fail-closed live registry check while this cursor sits at 0. Both are true at once.
 *
 * WHY IT NAMES A PATH RATHER THAN CREDITING THE POLL CARRIER. This page purges from the same
 * isolate whose reader it watches, so `expire()`'s ack can educate that reader on the spot — before
 * a poll tick is due, with every dial refused. Such an advance looks EXACTLY like a carrier rescue
 * from the outside: zero dials succeeded and the epoch moved, in ~2ms against a 5s cadence. A
 * dial-counter check cannot tell the two apart; only the epoch measured immediately after the ack
 * can, which is why `PendingPurge` carries it — and why crediting the carrier without that evidence
 * would be a fabrication, an unsupported claim this row must not make.
 *
 * WHY IT WATCHES THE CURSOR YET POINTS ELSEWHERE FOR CONVERGENCE. `appliedEpoch` is a bus-frame
 * cursor; the fail-closed live check moves no cursor. So a reader that has learned the purge by live
 * check — the verdict row beside this one reads `stale` against the right soft epoch — can leave
 * this cursor at 0, and a row that reported that as a reader which has not caught up would libel the
 * system it watches.
 *
 * So: it names the number it watches, and it names the path it can establish. What it will never do
 * is convert the absence of a frame into a claim about correctness.
 */
function rescueRow(input: NarrationInput): NarrationRow {
  const { rescue, awaitingRescueForMs } = input;
  let measured: string;
  if (rescue !== null) {
    measured = `${ms(rescue.afterMs)} — applied epoch moved ${rescue.fromEpoch} → ${rescue.toEpoch}; ${PATH_NOTE[rescue.path]}`;
  } else if (awaitingRescueForMs === null) {
    measured = "no purge issued this episode";
  } else {
    measured = `no frame in ${ms(awaitingRescueForMs)} — this cursor has not moved. ${waitingNote(input.witness)}`;
  }
  return {
    documented: `the CONTIGUOUS bus-frame epoch — NOT a measure of whether the reader knows the purge (the verdict row is). The reader re-reads the mirror pointer on a ${ms(POSTURE.maxSyncLagMs)} jittered cadence — THIS DEPLOYMENT'S setting, turned down from the shipped ${ms(POSTURE.shippedMaxSyncLagMs)} to make the poll watchable. It shortens only the term this deployment controls: the honest bound is that cadence PLUS the flush window PLUS __state KV propagation, which is documented as up to 60 seconds or more with no ceiling. This runs on local Miniflare, where that last term is nearly free — so this number is a floor, not a forecast.`,
    label: "purge → bus-frame cursor",
    measured,
  };
}

/** Whether the factory ran — the half of the read-cost row that says what the number IS. */
function fillNote(filled: boolean): string {
  return filled
    ? "the factory RAN: this read was a fill, and the origin round-trip is inside this number"
    : "served from a tier; no factory run";
}

/** What the read cost, measured at the app's edge, and whether it was a fill. */
function readCostRow(input: NarrationInput): NarrationRow {
  const measured =
    input.readMs === null
      ? "not timed this request"
      : `${ms(input.readMs)} — ${fillNote(input.filled)}`;
  return {
    documented:
      "the price of E13's posture, and the point of the trade: a suppressed L2 read is a plain miss, and a miss is a fill — so the caller pays an origin round-trip instead of receiving an error. Measured at this app's edge with the wall clock, over the whole read. One run on one machine is an anecdote, not a latency claim.",
    label: "read cost",
    measured,
  };
}

/** The rows that do not come from the witness's testimony — real observations either way, so they
 * survive an arm that has no entry to describe. */
function observedRows(input: NarrationInput): readonly NarrationRow[] {
  return [
    readCostRow(input),
    rescueRow(input),
    interceptionRow(input),
    heartbeatRow(input),
    eventsRow(input),
    {
      documented: `nothing is expected to be noticed before ${ms(POSTURE.suspicionAfterMs)} of OBSERVED silence; the reader's ticks are request-coupled, so detection latency is bounded below by the gap between requests, not just by the threshold`,
      label: "fault armed for",
      measured:
        input.armedForMs === null ? "nothing armed" : ms(input.armedForMs),
    },
  ];
}

/** The reader's own state — true on every arm, because the reader has it whether or not the store
 * answered. */
function readerRows(explanation: Explanation): readonly NarrationRow[] {
  return [
    {
      documented:
        "advances only on contiguously applied frames — a gap is never skipped",
      label: "reader applied epoch",
      measured: String(explanation.reader.appliedEpoch),
    },
    channelRow(explanation),
    {
      documented: `may raise once OBSERVED silence reaches ${ms(POSTURE.suspicionAfterMs)} (3× the heartbeat interval, E5). Raise-only, and derivative: a fast sampler cannot witness ${ms(POSTURE.suspicionAfterMs)} of silence in less than ${ms(POSTURE.suspicionAfterMs)}. A reader whose channel never came up is NOT suspect — it has no gap; read the channel row for that.`,
      label: "reader suspicion",
      measured: explanation.reader.suspect ? "SUSPECT" : "not suspect",
    },
    {
      documented:
        "a terminal resync verdict — the reader has stopped claiming to know",
      label: "reader terminal",
      measured: explanation.reader.terminal ? "TERMINAL" : "not terminal",
    },
  ];
}

/** The request-scoped dependency attribution, when a render collector is bound. */
function dependencyRow(
  dependencies: readonly ExplainedDependency[]
): NarrationRow {
  return {
    documented:
      "request-scoped attribution from the bound render collector — it lives and dies with this request; there is no persisted per-key dependency graph to consult (E27). The scope beside each tag is what E23's shared-cacheability gate reads.",
    label: "request dependencies",
    measured:
      dependencies.length === 0
        ? "none recorded this request"
        : dependencies
            .map(
              (dependency) =>
                `[${dependency.tags.join(" ")}] ${dependency.scope.kind}`
            )
            .join(" · "),
  };
}

/** The label every non-`present` arm reports under — one label, because the three arms are three
 * ANSWERS to one question rather than three different subjects. */
const WITNESS_LABEL = "explain() — the witness";

/** What the store said about the entry — one arm each, and they are structurally different answers
 * rather than three shades of the same one. */
function storyRows(explanation: Explanation): readonly NarrationRow[] {
  if (explanation.kind === "read-failed") {
    // THE ARM THE FLAGSHIP SCENE LANDS ON when nothing else holds the key. A tier's read failed and
    // was suppressed, and no other tier answered — so whether the entry exists is UNKNOWN. Calling
    // it `absent` would be the witness laundering an error into an answer: "I could not look" is not
    // "it is not there".
    return [
      {
        documented:
          "a tier's read failed and was suppressed (E13), and no other tier held the key — so whether it exists is unknown. Distinct from absence because the difference is real: one is the store answering `nothing`, the other is the store not answering. It carries no tier and no identity: a probe that could not read observed neither.",
        label: WITNESS_LABEL,
        measured:
          "READ-FAILED — the store did not answer, so the witness will not say whether the entry is there",
      },
    ];
  }
  if (explanation.kind === "absent") {
    return [
      {
        documented:
          "every tier was read and none holds the key. A REPORTABLE state, not an error — `explain` never throws for a missing key (E27).",
        label: WITNESS_LABEL,
        measured: `ABSENT — no entry (tier: ${explanation.tier}); a miss is a fill, not a stale serve`,
      },
    ];
  }
  return [
    {
      documented:
        "the tier the entry was FOUND in — an observation over L1 then L2, never a hydrating read (E27)",
      label: "serving tier",
      measured: explanation.tier,
    },
    {
      documented:
        "`unknown` is a real answer, not a gap: it may resolve to a live registry check rather than a serve (E9). Validity is epoch comparison, never a wall clock.",
      label: "verdict + evidence",
      measured: verdictLine(explanation),
    },
    {
      documented:
        "read from the stored bytes; a value the store never carried is reported absent",
      label: "stored ttl",
      measured: ttlLine(explanation),
    },
    {
      documented:
        "as stored, preserving the readable-vs-unreadable distinction — a scope that is readable AND exactly `pub` is the only thing that makes a response shared-cacheable; absent, malformed, or unreadable is non-public and poisons it (E23)",
      label: "stored scope",
      measured: scopeLine(explanation),
    },
  ];
}

/**
 * Turn one `explain()` witness statement into the console's rows. Every row pairs what was measured
 * with what is documented; no row concludes anything about the pairing.
 */
export function narrate(input: NarrationInput): Narration {
  const { witness } = input;
  if (!witness.ok) {
    // The witness RAISED. This is NOT a scene outcome: no dial in this app can produce it, because
    // the KV adapter's read classifier is total — every rejection it sees becomes a named,
    // suppressed read failure, which surfaces as the `read-failed` ARM rather than as a throw. This
    // is the app-edge guard for a failure nobody classified, and it prints the raise rather than
    // filling the table with plausible blanks. The rows that do NOT come from the witness are still
    // real observations, so they stay.
    return {
      caveat: NO_SLO_CAVEAT,
      judgments: deriveJudgments(input),
      lifetimeCaveat: LIFETIME_CAVEAT,
      rows: [
        {
          documented:
            "`explain` is documented never to throw for a missing key, and never to launder an unreadable store into an absence — a suppressed read reports as its own arm. A raise here means something no classifier named, which is worth seeing raw.",
          label: WITNESS_LABEL,
          measured: `RAISED — ${witness.error}`,
        },
        ...observedRows(input),
      ],
    };
  }
  const { explanation } = witness;
  const dependencies = explanation.requestDependencies;
  return {
    caveat: NO_SLO_CAVEAT,
    judgments: deriveJudgments(input),
    lifetimeCaveat: LIFETIME_CAVEAT,
    rows: [
      ...storyRows(explanation),
      ...readerRows(explanation),
      ...observedRows(input),
      ...(dependencies === undefined ? [] : [dependencyRow(dependencies)]),
    ],
  };
}
