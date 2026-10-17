// The chaos SCRIPT — three faults, written down.
//
// The deterministic fakes expose a chaos surface the invariant suite drives: drop the next frames,
// stall a key, disconnect, crash an isolate, skew a clock. That surface is bound to a simulated
// clock and to the fakes' own internals, so it cannot be pointed at real Cloudflare bindings and
// this demo does not try. What travels is the SCRIPT — which faults are worth showing, and what
// "resolving conservatively" looks like — not the injector. The injector out here is the pair of
// binding wrappers beside this file, and the faults below are the only three they can express.
//
// EACH SCENE DECLARES WHAT MAY FOLLOW, NOT WHAT WILL. The `mayHappen` lines are what this
// deployment's documentation permits — bounds, not promises, and deliberately phrased as the
// permissions they are. The demo measures what actually happened and prints the two side by side.
// It never asserts that the measurement satisfied the bound, because a demo that asserted an SLO
// would be a demo that fails on a slow afternoon, and the failure would be the demo's rather than
// the system's. Numbers first, promises second.

import { mirrorKeys } from "@astilba/cache/registry";

import type { FaultDials } from "./dials";

/** The three faults the exit line names. */
export type SceneId = "backend-kill" | "bus-drop" | "registry-outage";

/** Every scene id, in the order the console lists them. */
export const SCENE_IDS: readonly SceneId[] = [
  "backend-kill",
  "bus-drop",
  "registry-outage",
];

/** Is this string one of the three scene ids? The route's `:sceneId` param arrives as a bare
 * string, so the boundary that turns it into a `SceneId` is here, once. */
export function isSceneId(value: string): value is SceneId {
  return (SCENE_IDS as readonly string[]).includes(value);
}

/** One fault, its motivation, and what the docs permit to follow it. */
export interface Scene {
  readonly id: SceneId;
  readonly title: string;
  /** What the demo does to the bindings — the fault itself, in one line. */
  readonly injects: string;
  /** Why this fault is worth a scene. */
  readonly premise: string;
  /** What the documentation says MAY follow. Permissions, never guarantees — the demo prints these
   * beside what it measured and draws no conclusion about whether they were met. */
  readonly mayHappen: readonly string[];
  /**
   * Does this scene's episode boot a reader of its OWN, minted the moment the fault is armed?
   *
   * A channel that never established is a fact about a reader's LIFETIME, and the isolate's
   * long-lived reader came up on the first request anyone made — so no sequence of arming and
   * reloading can ever show one on it. A scene whose subject IS that state has to own a lifetime
   * that began under the fault. The flag is here rather than assumed everywhere because the demo's
   * default posture is the honest one: talk to the reader the app actually runs, and mint a fresh
   * one only where the scene's whole argument is about a reader's first moments.
   */
  readonly bootsOwnReader: boolean;
  /**
   * The instruction this scene's Purge button carries, or `null` where this scene does not instruct
   * a purge at all — in which case the page does not offer the button.
   *
   * A CONSOLE BUTTON IS AN INSTRUCTION whether or not the copy mentions it, so a flow that is not
   * part of a scene's argument does not get a control on that scene's page. Cutting the sentence and
   * leaving the button would be cutting the caption, not the flow.
   */
  readonly purgeInstruction: string | null;
  /** Apply this scene's fault to the dials. Every scene starts from a healed state. */
  arm(dials: FaultDials, context: SceneContext): void;
}

/** What a scene needs from the running app to arm itself. Not exported: `armScene` moved to
 * `app/world.ts` (the one-door heal), which passes this shape as an inline literal, so the only
 * consumer left is the `Scene.arm` signature in this file. */
interface SceneContext {
  /** The registry name — the one identity string the hero factory derives everything from, and the
   * scope of the mirror keyspace a Coordinator outage takes with it. */
  readonly registryName: string;
}

/**
 * The replication mirror's key prefix for `registryName`, taken from the library's OWN key formatter
 * rather than re-spelled here. The mirror's grammar is the library's to define and to change; a demo
 * that hard-coded it would keep "working" after a change by faulting nothing at all — a scene that
 * intercepts zero reads is indistinguishable, on screen, from a system that shrugged off the fault,
 * and that is the most dishonest thing this demo could do. Deriving it from the formatter means a
 * regrammar breaks the demo loudly instead; the wrapper's counters and the scene's own test are the
 * belt to this brace.
 */
function mirrorPrefix(registryName: string): string {
  const { stateKey } = mirrorKeys(registryName);
  const POINTER_SUFFIX = "state";
  return stateKey.slice(0, stateKey.length - POINTER_SUFFIX.length);
}

const SCENES: Readonly<Record<SceneId, Scene>> = {
  // THE FLAGSHIP. A suppressed L2 read is a plain MISS (the read half): the adapter's read
  // classifier is total, so there is no rejection it declines to name, and the kernel rethrows only
  // what nobody classified. This scene drives that path — the durable tier stops answering, the
  // library treats the failed read as a miss, the origin refills, the caller pays latency instead of
  // receiving an error, and nothing false is served. A raw store error surfacing to the caller as a
  // phantom hit would be the opposite behavior; the scene exists to show it does not happen. Every
  // line below is checked against the running app before it is printed.
  "backend-kill": {
    arm(dials) {
      dials.kv.failReads = true;
      dials.kv.failWrites = true;
    },
    bootsOwnReader: false,
    id: "backend-kill",
    injects: "the KV binding rejects every read and every write",
    // EVERY LINE IS DIFFERENTIAL, and that is the repair. A bullet that credits the dead tier for
    // something that happens identically with KV alive is not describing the fault; it is describing
    // the cache and pointing at the fault while it does. So each line below names what CHANGES under
    // the fault, and where the control behaves the same, the line says so rather than taking credit.
    mayHappen: [
      "a failing L2 read may be classified, suppressed, and absorbed into a plain miss — never routed to unknown, never surfaced as an error (E13). This is the divergence: with the store alive that read is a hit, and the whole scene is what the library does INSTEAD of erroring",
      "so every read that misses L1 may become a fill, where the control would have been served from L2 — the origin pays what the durable tier would have. That cost is the trade, and it is what the fault actually causes",
      "reads may be served from L1 — an isolate-local tier the fault never touched, so it behaves exactly as it would with KV alive. The refill lands there and the outage may cost ONE origin call however long it runs; watch the factory counter stop climbing. Nothing here is the dead tier's doing: it is the tier the dead one is not",
      "a fill's write-back may be suppressed rather than fail the caller, and `l2_write_error` may appear in the telemetry row — the control writes back silently and logs nothing, so this row IS the fault",
      "`explain()` may report `read-failed` rather than `absent`: a store that did not answer is not a store that said `nothing`. With KV alive this arm is unreachable, which is what makes it worth a row",
    ],
    premise:
      "the durable tier is the one every isolate shares. When it goes, the question is whether the cache degrades or invents — and the answer costs latency, which is the whole trade.",
    // NO PURGE ON THIS SCENE. The purge-under-outage flow is not part of this scene's argument, and
    // a button is an instruction whether or not a bullet mentions it.
    purgeInstruction: null,
    title: "Backend kill — the durable store stops answering",
  },
  // THE DEAF READER, SAYING SO. This is the arc's argument on camera. The failure worth preventing
  // was never "the bus drops" — it is a reader that stops learning and still believes it is healthy.
  // This page could once show exactly that: applied epoch 0, `suspect: false`, a serene verdict, and
  // a socket that had never once connected. Nothing on the screen could tell that apart from a quiet
  // afternoon. The reader now tracks its channel as a declared state, says `never-established` out
  // loud through `explain()`, and emits a raw `bus_dial_failed` on every failed lazy dial — so the
  // console has something true to print. And the purge still arrives, because the poll carrier is a
  // second path to the same truth: the page measures how long it takes and prints the epochs on both
  // sides of the number.
  "bus-drop": {
    arm(dials) {
      dials.bus.dialRefused = true;
    },
    // THE RIG, AND THE SCENE DOES NOT WORK WITHOUT IT. `never-established` is a fact about a
    // reader's whole life, and this isolate's long-lived reader came up on the first request anyone
    // made — so arming the dial and reloading could never show one. Arming this scene mints a reader
    // whose first use lands on the far side of the refusal. The copy says so.
    bootsOwnReader: true,
    id: "bus-drop",
    injects: "the Coordinator's WebSocket dial refuses to connect",
    mayHappen: [
      "THE RIG, STATED: arming this scene boots a reader of its own, minted the instant the dial was set — so its first dial is its first act and it meets the refusal. `never-established` is a property of a reader's LIFETIME, and a page can only show one honestly by owning a lifetime that started under the fault. The isolate's long-lived reader, which every other page here narrates, came up before you armed anything; healing gives it back",
      "that reader's channel may report `never-established` — a declared state, not silence, and the one thing that tells a dead bus from a quiet healthy one (E28)",
      "each failed lazy dial may emit a raw `bus_dial_failed` — the alternative to which is silence",
      "the reader may NOT be suspect, and that is not the contradiction it looks like: suspicion is about a GAP in a channel, and a channel that never came up has no gap. Read the channel row, not the suspicion row",
      "the transport reports the loss and the redialing driver backs off — it does not retry in place, and no timer performs the redial",
      "a purge still arrives with the bus dead — but read WHICH PATH the rescue row names before crediting the poll carrier. Purging from this page teaches this isolate's own reader through `expire()`'s ack, instantly, with every dial refused: that is an own-ack, not a rescue, and the row says so. A carrier rescue needs a reader that did NOT issue the purge, which needs a second isolate this page does not have",
    ],
    premise:
      "invalidation travels over one socket. When it will not open, the reader stops learning — and a reader that does not know it stopped learning is the failure worth preventing. Watch it say so.",
    purgeInstruction:
      "Purge the tag — then read the rescue row's PATH before crediting anything. This page purges from the same isolate whose reader it watches, so the ack teaches that reader on the spot: an own-ack, not a rescue.",
    title:
      "Bus drop — the invalidation socket will not open, and the reader says so",
  },
  "registry-outage": {
    arm(dials, { registryName }) {
      // The reader's two inputs, both gone: no frames (the socket will not open) and no mirror to
      // resync from (its keyspace stops answering). The cache's own entries stay readable — that is
      // what makes this a REGISTRY outage rather than a store outage, and the distinction is the
      // whole scene: the data is right there, and the system still declines to trust it.
      dials.bus.dialRefused = true;
      dials.kv.unreadablePrefix = mirrorPrefix(registryName);
    },
    bootsOwnReader: false,
    id: "registry-outage",
    injects:
      "the Coordinator's dial refuses AND its mirror keyspace stops answering reads",
    mayHappen: [
      "the reader loses both inputs — no frames, no mirror — and its knowledge stops at the last epoch it applied",
      "observed silence past the suspicion threshold may raise suspicion",
      "entries whose validity cannot be established may report `unknown` — and `unknown` may resolve to a live registry check rather than a serve (E9)",
    ],
    premise:
      "the entries are readable and the authority that says whether they are still true is not. Every stale serve in the world begins here.",
    purgeInstruction:
      "Purge the tag — the invalidation path runs through the machinery this scene broke, so a purge that cannot be recorded fails loudly rather than being quietly lost. That refusal is the scene.",
    title: "Registry outage — the Coordinator is unreachable",
  },
};

/** The scene with this id. */
export function sceneOf(id: SceneId): Scene {
  return SCENES[id];
}

/** Every scene, in console order. */
export function allScenes(): readonly Scene[] {
  return SCENE_IDS.map((id) => SCENES[id]);
}

// ARMING LIVES IN `app/world.ts`, NOT HERE, and the move is the point rather than a tidy-up. This
// file is the CATALOGUE: what each fault is, what it touches, what the docs permit to follow. Arming
// is a transition over the whole EPISODE — the dials, yes, but also the counters, the event log, the
// rescue stopwatch and the reader the episode may boot — and this file knows about none of those.
// `armScene` used to live here and clear the dials alone, which is exactly the shape a partial reset
// takes when it is written next to the only piece its own file can see.
