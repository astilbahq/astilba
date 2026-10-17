import type {
  Bus,
  BusEvent,
  BusFrame,
  Registry,
  Store,
  Subscription,
  TagChange,
} from "./drivers";
import type { Envelope } from "./envelope";
import { RegistryUnavailableError, UnknownTagError } from "./errors";
import type { Consistency, TagKnowledge, Validity } from "./knowledge";
import { mirrorKeys } from "./registry/mirror-keys";
import type { MirrorKeys, StatePointer } from "./registry/mirror-keys";
import { isReservedTag } from "./tags";
import { TELEMETRY_EVENTS } from "./telemetry";
import type { StrongDegradedReason, TelemetryEvent } from "./telemetry";

/** The one reason class a `strong_degraded` emit carries today. */
const STRONG_DEGRADED_REGISTRY_UNREACHABLE: StrongDegradedReason =
  "registry_unreachable";

// The dual-epoch invalidation core. This module owns:
// • the per-isolate tri-state tag-knowledge map fed by the bus;
// • bus-frame contiguity + gap/reconnect resync;
//   • cold-path resync from the __state pointer + __delta batches durably in L2;
// • the validity table (fresh/stale/dead/unknown) over an envelope's tags;
// • the write-back fence over LOCAL knowledge — opportunistic: it fences a purge
// already delivered here during a fill. the goal (never SERVE a value born
//     against a durable purge) is anchored by the STRONG fill's initiation-time born-mint
// (dominates every pre-initiation purge); a SUBSEQUENT read is guarded at ITS
// own initiation (fail-closed resolution + the strong-read live check, INV-12).
//
// No ambient time/randomness lives here — timing comes from the injected clock in
// cache.ts. A registry-absent cache never constructs this machinery,
// so the codec-only path (INV-8/27) validates every entry FRESH unchanged.
//
// KEY SCOPING: the mirror keys this reader resyncs off are registry-scoped
// (`mirrorKeys(regId)`, the SAME formatter the writer addresses with) and the pointer
// payload echoes `regId`; the reader asserts the echo matches its own configured id, so a
// scheme/prefix divergence — or a foreign registry's pointer — reads as an empty keyspace
// and fails closed, never blessing another registry's state.

/** One tag's applied watermark knowledge (`known` arm of `TagKnowledge`). */
interface KnownWatermark {
  softEpoch: number;
  hardEpoch: number;
}

/**
 * The THROWING arm of `UnknownResolution`: resolve to an error, and remember which
 * switch chose it. Two switches reach this arm — an explicit `unknownPolicy: 'error'` and the
 * `takedownSensitive` override that outranks it — and they are indistinguishable in effect but not
 * in diagnosis. Carrying the armer is what lets the error name the switch that actually fired
 * instead of guessing; the alternative is a second copy of the precedence rule somewhere else.
 */
interface UnknownErrorResolution {
  readonly kind: "error";
  readonly armedBy: "unknownPolicy" | "takedownSensitive";
}

/** How fail-closed `unknown` resolves at read time. */
export type UnknownResolution =
  | "registry-check"
  | "miss"
  | UnknownErrorResolution;

/**
 * The live channel's state, as the reader has observed it. A LOCAL FACT — three states,
 * because two would hide the one that matters:
 *
 *   - `never-established` — no `hello` has ever arrived on any socket. The reader has NEVER had a
 *     live channel, so it runs degraded and SAYS so: what keeps it correct meanwhile is the poll
 *     carrier plus failing closed on any unknown tag, and each of those checks teaches the map
 * something it keeps. This is NOT suspicion — the suspect flag is for a map that
 *     knows it MISSED something; a reader that never connected has missed nothing, because it never
 *     had a channel to miss anything on. Conflating them
 *     would either make every cold boot look gapped or make a dead bus look healthy; it is its own
 *     state precisely so it can be neither.
 *   - `established` — a `hello` arrived and the socket is up.
 *   - `lost` — a socket that HAD established is now down. Distinguished from `never-established`
 *     because the two have different remedies and different histories: one has knowledge to resync
 *     against a head it once heard, the other has never heard one.
 */
export type ChannelState = "never-established" | "established" | "lost";

/** The channel's starting state, and the one a reader returns to saying rather than assuming: it has
 * never heard a `hello`. Named once so the three sites that compare against it cannot drift. */
const NEVER_ESTABLISHED: ChannelState = "never-established";

/** The outcome of resolving an entry's validity given the isolate's knowledge. */
export interface ValidationResult {
  readonly validity: Validity;
  /** The epoch a live check performed by THIS validation verified, or 0 if
   * it resolved from local knowledge alone. A same-call refill uses it so a behind
   * isolate does not fence its own legitimate refetch. */
  readonly checkedThrough: number;
}

/**
 * The outcome of one bounded resync pass over the durable mirror. Surfaces the
 * status the old `#replayBatches` computed and discarded, so the SHELL (2b-2-b) can
 * schedule the retry ladder off a decision the pure kernel makes here — never a sleep:
 *
 *   - `converged` — the delta chain replayed contiguously to `pointer.lastBatch` (the
 *                          durable frontier). Whether that ALSO clears suspicion is a SEPARATE
 *                          head-anchored decision (`#tryConverge`): a pointer lagging the live
 *                          head converges the delta pass yet stays suspect.
 *   - `transient-miss` — a blessed delta at `seq` (≤ `lastBatch`) is not yet visible;
 *                          it may simply be LAGGING, so this is the RETRY signal. A read
 * arriving now still fails closed — NEVER served through.
 *   - `needs-snapshot` — a hole persists and the caller's delta-retry budget is spent
 *                          (`deltasExhausted`); the reader signals the shell to escalate
 *                          to the snapshot bridge (`resyncViaSnapshot`).
 *   - `terminal-gap` — the chain is UNFILLABLE (the blessed snapshot is gone AND the
 *                          delta chain below it is broken); permanent, fail-closed, never
 * retried and never served through.
 */
export type ResyncStatus =
  | "converged"
  | { readonly kind: "transient-miss"; readonly seq: number }
  | "needs-snapshot"
  | "terminal-gap";

/** Build the `transient-miss` retry signal for `seq` (the single runtime literal source). */
function transientMiss(seq: number): ResyncStatus {
  return { kind: "transient-miss", seq };
}

/** The shell's escalation gate for the DELTA pass. Time/retries live in the shell
 * (2b-2-b); the kernel only reads this boolean, so the pass stays pure and drivable. */
export interface ResyncPolicy {
  /** `false` (validate's fast pass + early shell attempts): a hole yields `transient-miss`
   * (retry — the delta may be lagging). `true` (the shell's bounded delta-retry budget is
   * spent): a hole yields `needs-snapshot` — escalate to the snapshot bridge. */
  readonly deltasExhausted: boolean;
}

/** The shell's escalation gate for the SNAPSHOT bridge. */
export interface SnapshotPolicy {
  /** `false` (early snapshot attempts): a blessed-but-missing snapshot yields `transient-miss`
   * (brief retry — the snap may be lagging). `true` (the shell's bounded snapshot-retry budget
   * is ALSO spent): the reader makes the PERMANENT ruling — full delta replay if the chain is
   * intact, else `terminal-gap`. */
  readonly snapshotExhausted: boolean;
}

/** Narrow an `unknown` to an index-readable object, or `undefined`. The
 * single narrowing assertion in this module: every field read off the result is
 * individually type-checked below before use, so the record view is sound. */
function parseValueObject(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== "object" || value === null) {
    return undefined;
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- narrowing a confirmed non-null object to an index-readable record; each field is `typeof`-checked at the call sites (readPointer/readFrame/readChange).
  return value as Record<string, unknown>;
}

/** Parse a stored JSON string to an object, or `undefined` on a malformed body. */
function parseJsonObject(raw: string): Record<string, unknown> | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return undefined;
  }
  return parseValueObject(parsed);
}

/** Read the `state` pointer shape from stored bytes. Malformed — including a
 * non-string or absent `regId` echo — ⇒ undefined (fail closed). */
function readPointer(raw: string): StatePointer | undefined {
  const o = parseJsonObject(raw);
  if (
    !o ||
    typeof o.epoch !== "number" ||
    typeof o.lastBatch !== "number" ||
    typeof o.regId !== "string"
  ) {
    return undefined;
  }
  const snapBatch = typeof o.snapBatch === "number" ? o.snapBatch : 0;
  const flushedAt = typeof o.flushedAt === "number" ? o.flushedAt : 0;
  return {
    epoch: o.epoch,
    flushedAt,
    lastBatch: o.lastBatch,
    regId: o.regId,
    snapBatch,
  };
}

/**
 * Read one watermark change from a parsed value. A CORRUPT change ⇒ `undefined`
 * (fail closed), DISTINGUISHED from a legitimately-absent optional axis:
 *   • a PRESENT-but-non-number `softEpoch`/`hardEpoch` (the field is on the object but is not a
 *     `number`) is corruption — dropping it and applying the rest would silently delete a purge
 * from history (a serve-dead vector under non-canonical KV writes);
 *   • an AXIS-EMPTY change (a `tag` but NEITHER a numeric `softEpoch` NOR a numeric `hardEpoch`)
 * violates the ≥1-axis rule.
 * An ABSENT optional axis stays legal — a change may legitimately touch a single axis. A corrupt
 * change holes its whole batch via `readFrame` (never a partial-apply); the caller fails closed.
 */
function readChange(value: unknown): TagChange | undefined {
  const rec = parseValueObject(value);
  if (!rec) {
    return undefined;
  }
  const { tag } = rec;
  if (typeof tag !== "string") {
    return undefined;
  }
  const soft = rec.softEpoch;
  const hard = rec.hardEpoch;
  // Present-but-non-number ⇒ corrupt (NOT an absent axis): the field is on the object yet not a
  // number, so it was garbled — fail the whole change closed rather than drop-and-apply-the-rest.
  if (
    ("softEpoch" in rec && typeof soft !== "number") ||
    ("hardEpoch" in rec && typeof hard !== "number")
  ) {
    return undefined;
  }
  // ≥1-axis rule: an axis-empty change (here, neither axis present) carries no watermark — invalid.
  if (typeof soft !== "number" && typeof hard !== "number") {
    return undefined;
  }
  return {
    tag,
    ...(typeof soft === "number" ? { softEpoch: soft } : {}),
    ...(typeof hard === "number" ? { hardEpoch: hard } : {}),
  };
}

/**
 * Read a `__delta` batch frame from stored bytes. Malformed ⇒ undefined. Exported so the
 * doBus wire codec (`src/adapters/cloudflare/wire.ts`) validates a wire-carried frame through the
 * SAME frame parser the KV-resync path uses — one parser for the durable mirror AND the live channel,
 * so a corrupt wire change fails closed exactly like a corrupt `__delta` byte (never a partial
 * apply). Additive export only: no reader branch changes, no behavior change (the corrupt-change
 * fail-closed semantics are pinned by `reader-corrupt-change-fail-closed`).
 */
export function readFrame(raw: string): BusFrame | undefined {
  const o = parseJsonObject(raw);
  if (
    !o ||
    typeof o.fromEpoch !== "number" ||
    typeof o.toEpoch !== "number" ||
    !Array.isArray(o.changes)
  ) {
    return undefined;
  }
  const changes: TagChange[] = [];
  for (const entry of o.changes) {
    const change = readChange(entry);
    if (!change) {
      // A SINGLE unreadable change makes the WHOLE FRAME unreadable. Partial-applying
      // the readable rest would silently drop the corrupt change's axis and record its tag as
      // known-clean — deleting a purge from history (serve-dead under non-canonical writes).
      // A frame that fails to parse is already handled downstream as a HOLE (#replayBatches /
      // #applyFrame), routing to the fail-closed ladder → snapshot → terminal machinery.
      return undefined;
    }
    changes.push(change);
  }
  return { changes, fromEpoch: o.fromEpoch, toEpoch: o.toEpoch };
}

/** Merge a frame's watermark changes into a knowledge map (max-on-write). */
function applyChanges(
  known: Map<string, KnownWatermark>,
  frame: BusFrame
): void {
  for (const change of frame.changes) {
    const w = known.get(change.tag) ?? { hardEpoch: 0, softEpoch: 0 };
    if (change.softEpoch !== undefined) {
      w.softEpoch = Math.max(w.softEpoch, change.softEpoch);
    }
    if (change.hardEpoch !== undefined) {
      w.hardEpoch = Math.max(w.hardEpoch, change.hardEpoch);
    }
    known.set(change.tag, w);
  }
}

/** The validity table over an entry's `bornEpoch` and per-tag knowledge. */
function classify(
  bornEpoch: number,
  knowledge: readonly TagKnowledge[]
): Validity {
  // any tag UNKNOWN → UNKNOWN (fail closed) — checked first.
  if (knowledge.some((k) => !k.known)) {
    return "unknown";
  }
  // any tag hardEpoch > bornEpoch → DEAD (unreadable, even in grace).
  if (knowledge.some((k) => k.known && k.hardEpoch > bornEpoch)) {
    return "dead";
  }
  // any tag softEpoch > bornEpoch → STALE (grace/SWR-eligible).
  if (knowledge.some((k) => k.known && k.softEpoch > bornEpoch)) {
    return "stale";
  }
  return "fresh";
}

/** The highest `throughEpoch` a live check verified (0 if none is `known`). */
function throughEpochOf(knowledge: readonly TagKnowledge[]): number {
  let max = 0;
  for (const k of knowledge) {
    if (k.known && k.throughEpoch > max) {
      max = k.throughEpoch;
    }
  }
  return max;
}

/** Default liveness heartbeat interval in ms — the cadence at which a live writer refreshes the
 * pointer's freshness field. The reader mirrors the coordinator's `REGISTRY_HEARTBEAT_MS`; the
 * 2b-2-b shell threads the real value at construction. Suspicion raises after `3 ×` this much
 * OBSERVED SILENCE — an elapsed-time derivative, sampling-rate-independent (not a tick count). */
const DEFAULT_HEARTBEAT_INTERVAL_MS = 30_000;
const SUSPICION_SILENCE_MULTIPLE = 3;

/**
 * The per-isolate invalidation state. Tracks tri-state tag knowledge
 * fed by the bus, the highest contiguously-applied epoch, and a `suspect` flag
 * raised on any gap/reconnect that a resync must clear before warm trust resumes.
 *Tags absent from the map are `unknown` and fail closed (invariant 11).
 */
export class Invalidation {
  readonly #registry: Registry;
  readonly #stateStore: Store;
  /** The bus, HELD as an address rather than a subscription. Construction wires; it does not
   * dial. The first read that needs the channel subscribes (`#ensureSubscribed`), inside the request
   * context that asked — which is the only context where a dial is permitted to happen at all. */
  readonly #bus: Bus;
  /** Telemetry for the reader's own raw facts — currently `bus_dial_failed`. Optional: a cache
   * built with no sink emits nothing, and the reader never depends on the emit for correctness. */
  readonly #emit: ((event: TelemetryEvent) => void) | undefined;
  /** This isolate's mirror-key scope, read from `registry.regId` at construction. It
   * both scopes the keys this reader resyncs off (`mirrorKeys(#regId)`) AND is the value the
   * pointer's echoed `regId` must equal — a mismatch reads a foreign keyspace and fails closed. */
  readonly #regId: string;
  #subscription: Subscription | null = null;
  /** `close()` was called: no further subscribe may happen (a lazy dial must not resurrect a closed
   * reader — the one way deferral could outlive its owner). */
  #closed = false;
  /** The live channel's state — see `ChannelState`. Starts at the honest answer: this reader
   * has never heard a `hello`, and until it does it says so rather than resting on silence. */
  #channel: ChannelState = NEVER_ESTABLISHED;

  /** tag → applied watermark; absence ⇒ `unknown` (tri-state). */
  readonly #known = new Map<string, KnownWatermark>();
  /** Highest contiguous epoch applied from bus frames / resync. Drives gap
   * detection — must stay strictly contiguous. */
  #appliedEpoch = 0;
  /** Highest epoch this isolate has VERIFIED from any authority — the contiguous bus
   * `#appliedEpoch` OR a live check's `throughEpoch`. This is the writer's
   * applied registry epoch for bornEpoch minting: a fill after a verified
   * live check must postdate what that check observed, so a legitimate refill on a
   * behind isolate is not fenced by its own recorded watermark. */
  #knownThroughEpoch = 0;
  /** Raised on a bus gap or reset; forces resync before warm trust. While raised,
   * every tag is treated as unknown at eventual reads — a possibly-gapped map is not the
   * merely-degraded warm map Q4 lets an isolate keep serving on. */
  #suspect = false;
  /** The max head DECLARED over the currently-open live channel during THIS suspect episode
   * — from a LOSS-revealing event only: `hello.head`, `gap.head`, or a NON-CONTIGUOUS
   * frame's `toEpoch`. A contiguous frame never declares (it only reaches toward a named head).
   * `null` until a head is declared this episode; a `reset` discards it (new episode), a `gap`
   * RAISES it (≥ the frame it lost) — so a hidden purge behind a hole is accounted before
   * convergence, never skipped (serve-dead — the shape). Convergence anchors
   * to THIS, never to pointer freshness. */
  #headThisEpisode: number | null = null;
  /** The `flushedAt` of the pointer at the last observation that ADVANCED it. `null` until
   * the first observation seeds the baseline. Compared DERIVATIVELY — "did it advance?" via
   * EQUALITY/`>` — never as an absolute-freshness anchor ( : `flushedAt` may not clear
   * suspicion nor date the map). */
  #lastFlushedAt: number | null = null;
  /** The `nowMs` of the previous observation — a MONOTONICITY guard so a stale/reordered tick
   * (`nowMs ≤` this) is a true no-op, NOT an age anchor (it is never compared to `flushedAt`).
   * `null` until the first observation. */
  #lastObservationMs: number | null = null;
  /** The `nowMs` at which the current unbroken SILENCE began — the last observation that seeded
   * the baseline or saw `flushedAt` advance. `nowMs − this` is the observed silence; reaching
   * `#suspicionAfterMs` RAISES suspicion (derivative, raise-only, never clears). `null` until the
   * first observation. */
  #silenceStartMs: number | null = null;
  /** Raise suspicion once OBSERVED SILENCE reaches this — `3 × heartbeatIntervalMs`, fixed at
   * construction. Elapsed silence is sampling-rate-INDEPENDENT: fast ticks cannot witness this much
   * silence in less than this much time, so a false raise is structurally impossible. */
  readonly #suspicionAfterMs: number;

  constructor(
    registry: Registry,
    stateStore: Store,
    bus: Bus,
    heartbeatIntervalMs: number = DEFAULT_HEARTBEAT_INTERVAL_MS,
    emit?: (event: TelemetryEvent) => void
  ) {
    this.#registry = registry;
    this.#emit = emit;
    // raise suspicion after 3 heartbeat intervals of OBSERVED SILENCE. Fixed here so
    // the derivative monitor is sampling-rate-independent — the shell (2b-2-b) threads the real
    // `REGISTRY_HEARTBEAT_MS`; a reader interval shorter than the coordinator's errs toward
    // suspicion (safe, Q4-priced), longer toward a late raise (ledger).
    this.#suspicionAfterMs = SUSPICION_SILENCE_MULTIPLE * heartbeatIntervalMs;
    // A replication-mirror store is MANDATORY here: this reader's honest job is to resync off
    // the `state`/`delta` mirror in L2, and without it a bus gap could never be cleared safely.
    // The store is required at construction (the caller, `buildInvalidation`, rejects a
    // registry+bus config that wires no L2), so `resync`
    // never faces an absent store: the old `if (!store) { suspect = false }` fail-OPEN is gone.
    this.#stateStore = stateStore;
    this.#regId = registry.regId;
    // THE BUS IS CAPTURED, NOT DIALED. Construction wires and nothing more: no subscribe, no
    // socket, no I/O of any kind. The platform forbids I/O at global scope, and one cache per isolate
    // — built at module scope, which is the placement the quickstart teaches — is only reachable if
    // this constructor stays cold. The subscription happens at `#ensureSubscribed`, on the first read
    // that needs the channel, inside that read's request context.
    this.#bus = bus;
  }

  /**
   * Subscribe to the invalidation bus if this reader has not already — THE LAZY DIAL,
   * and the deferral the reachability rule requires of construction. Idempotent and synchronous, so any entry point
   * may call it unconditionally; the dial itself is the transport's business and may complete long
   * after this returns.
   *
   * CALL IT FROM EVERY PATH THAT IS "USE" — the read path (`validate`), the tick path (`resync` /
   * `resyncViaSnapshot`), and the purge verbs, which are registry RPCs and therefore already inside
   * a request context. NEVER from `explain`: a witness that dialed would be testimony rather than
   * introspection, which is why `explainVerdict` is the one reader entry point that does not
   * appear on that list.
   *
   * NOTHING IS MISSED BY SUBSCRIBING LATE. A purge published before this reader was listening is not
   * lost: every establishment handshakes `reset` → `hello{head}`, which suspends warm trust and
   * anchors a resync against the durable `__state`/`__delta` mirror. The live channel is an
   * optimization over that mirror, never the sole path — so the cost of subscribing at first use is
   * one cold resync, and the benefit is an isolate that can be built at module scope at all.
   *
   * The subscriber consumes a BusEvent union: a `reset` (transport (re)establishment) or a `gap`
   * (dropped frame) marks the map suspect and forces a resync before warm trust resumes; a `frame`
   * takes the contiguity path; a `lost` records the channel's own state.
   */
  ensureSubscribed(): void {
    if (this.#subscription !== null || this.#closed) {
      return;
    }
    this.#subscription = this.#bus.subscribe((event: BusEvent): void => {
      this.#applyEvent(event);
    });
  }

  get appliedEpoch(): number {
    return this.#appliedEpoch;
  }

  /** Whether this isolate currently DISTRUSTS its warm map — read-only. A read
   * arriving while suspect fails closed (the gate); the shell reads it to know whether
   * the ladder still has work. Raised by a bus gap/reset OR the derivative heartbeat miss;
   * cleared ONLY by `#tryConverge` reaching a live-channel head (never by pointer freshness). */
  get suspect(): boolean {
    return this.#suspect;
  }

  /** The live channel's state — read-only exposition of state this reader already tracks for
   * its own use, never new bookkeeping added for the witness. */
  get channel(): ChannelState {
    return this.#channel;
  }

  close(): void {
    this.#closed = true;
    this.#subscription?.close();
    this.#subscription = null;
  }

  /** Advance the contiguous applied epoch (and the verified high-water) to `toEpoch`. */
  #advanceApplied(toEpoch: number): void {
    this.#appliedEpoch = toEpoch;
    this.#knownThroughEpoch = Math.max(this.#knownThroughEpoch, toEpoch);
  }

  /**
   * Dispatch one bus event. Convergence anchors to a head DECLARED this episode,
   *from exactly the LOSS-REVEALING events: a `hello` (the channel head), a `gap`
   * (its `head`, ≥ the frame it lost), or a NON-CONTIGUOUS frame (its `toEpoch`, the hole
   * it proves). A CONTIGUOUS frame does NOT declare — it only reaches toward a head already named.
   * A `reset` discards the anchor (a new episode; the following `hello` re-declares). The kernel,
   * not the bus, owns contiguity validation.
   */
  #applyEvent(event: BusEvent): void {
    switch (event.kind) {
      case "frame": {
        // The contiguity path (M2 — INV-19). A CONTIGUOUS frame does not declare; a
        // NON-CONTIGUOUS one declares its `toEpoch` (the hole it reveals) — see `#applyFrame`.
        this.#applyFrame(event.frame);
        return;
      }
      case "hello": {
        // The handshake's head declaration. Anchor convergence to it — an
        // idle channel's head equals the applied epoch, so a caught-up isolate reconverges
        // at once; a behind isolate resyncs up to it before warm trust resumes.
        //
        // It is ALSO the one event that proves the channel is up: `hello` is the DO echoing
        // our scope and declaring its head, which no half-open socket can produce. So establishment
        // is recorded here and nowhere else — a dial that resolved a socket has not established
        // anything until the far end speaks.
        this.#channel = "established";
        this.#headThisEpisode = Math.max(
          this.#headThisEpisode ?? 0,
          event.head
        );
        this.#tryConverge();
        return;
      }
      case "lost": {
        // The channel is DOWN — a dial that never came up, or an established socket that
        // closed. Recorded, emitted, and NOTHING ELSE: suspicion is the posture and a down
        // channel is not a gapped map (see `ChannelState`). A reader that has never established
        // stays `never-established` — it did not LOSE a channel it never had, and the distinction is
        // the whole reason this state exists.
        //
        // THE TWO CLOSES ARE DIFFERENT FACTS, and this is the ONE place that tells them apart. A
        // transport reports `lost` whenever a socket ends, whether the dial never came up or a
        // working channel dropped — the report cannot distinguish them, because from the socket's
        // side both are just an ending. The reader can, from state it already tracks: `established`
        // is set by a `hello` and by nothing else, so a `lost` arriving while established IS the
        // healthy socket dropping, and that is not a dial failure. Anything else — never-established,
        // or a redial after a previous loss — is an attempt that failed to come up, which is exactly
        // what the counter counts.
        //
        // The emit is what makes the failure a FACT rather than silence. It is bounded by the
        // redial's own pacing (a dial is attempted only when a tick finds one due), so this cannot
        // become a hot loop: the event's rate is the tick's rate.
        const healthyClose = this.#channel === "established";
        this.#channel =
          this.#channel === NEVER_ESTABLISHED ? NEVER_ESTABLISHED : "lost";
        if (!healthyClose) {
          this.#emit?.({ type: TELEMETRY_EVENTS.bus_dial_failed });
        }
        return;
      }
      case "gap": {
        // A dropped/lost frame: warm trust is suspended and the gap DECLARES its head —
        // ≥ the `toEpoch` of the frame it lost. Raising the anchor to it defers convergence until
        // the applied epoch reaches it (via resync / later frames), so a hidden purge behind the
        // hole is accounted, never skipped (serve-dead — the shape). `gap.head`
        // is monotone (≥ any prior declaration), so max-without-reset here equals a reset-then-
        // declare. No `tryConverge`: `gap.head` > `appliedEpoch` by construction (an unapplied loss).
        this.#suspect = true;
        this.#headThisEpisode = Math.max(
          this.#headThisEpisode ?? 0,
          event.head
        );
        return;
      }
      case "reset": {
        // A transport (re)establishment: a NEW episode. Discard the anchor; the following
        // `hello` re-declares the channel's head. A silent transport drop surfaces as a
        // `reset` (→ `hello`), not a `gap`, so a `reset` must not carry a stale head forward.
        this.#suspect = true;
        this.#headThisEpisode = null;
        return;
      }
      default: {
        // Exhaustiveness guard: a new `BusEvent` kind must add a case above — this fails
        // to COMPILE if one is missing (`event` is no longer `never`), never silently swallowed.
        const unreachable: never = event;
        return unreachable;
      }
    }
  }

  /** Apply a bus frame if contiguous; otherwise raise suspicion. */
  #applyFrame(frame: BusFrame): void {
    if (frame.fromEpoch !== this.#appliedEpoch) {
      // A gap detected in-band: a non-contiguous frame PROVES a hole. Mark suspect and DECLARE
      // its `toEpoch` — a loss-revealing event declares a head ≥ the loss, raising the
      // anchor ABOVE the applied epoch so convergence defers until the hole is filled. The frame
      // is NOT applied (M2 — INV-19). No `tryConverge`: `toEpoch` > `appliedEpoch` by construction.
      this.#suspect = true;
      this.#headThisEpisode = Math.max(
        this.#headThisEpisode ?? 0,
        frame.toEpoch
      );
      return;
    }
    applyChanges(this.#known, frame);
    this.#advanceApplied(frame.toEpoch);
    // A CONTIGUOUS frame does NOT declare: its `toEpoch` == the new `appliedEpoch`, so
    // self-declaring would be "converge wherever I stand" — clearing a still-open hole a reordered
    // higher frame revealed. It only reaches toward a head an authoritative declaration (hello /
    // gap / a non-contiguous frame above it) already named.
    this.#tryConverge();
  }

  /**
   * Clear suspicion iff a head was DECLARED over the live channel this episode (hello/frame)
   * AND the applied epoch has reached it. `#appliedEpoch` advances only by contiguous
   * application, so `applied >= head` already encodes hole-free reach (β) — no separate holes
   * check. Pointer freshness never arbitrates clearing: a heartbeat refreshes the pointer's
   * freshness field without minting epochs, so freshness motion is not evidence the reader has
   * caught up; only a live-channel head declaration anchors convergence. Bus-down /
   * poll-only declares no head ⇒ sticky suspect (posture, priced).
   */
  #tryConverge(): void {
    if (
      this.#suspect &&
      this.#headThisEpisode !== null &&
      this.#appliedEpoch >= this.#headThisEpisode
    ) {
      this.#suspect = false;
    }
  }

  /**
   * Cold/resync path — the bounded single DELTA pass. Read the `__state`
   * pointer from L2 and replay the `(applied, lastBatch]` delta batches — durably write-once
   * in L2 even when the bus frame was dropped — applying the contiguous prefix (β), which
   * advances `#appliedEpoch`. A missing pointer or a hole leaves affected knowledge `unknown`
   * (fail closed). Resync does NOT clear suspicion off pointer freshness: a heartbeat refreshes the
   * freshness field without minting epochs, so freshness motion is not evidence of catch-up.
   *It only APPLIES the durable prefix; clearing is `#tryConverge`'s job, and fires iff
   * a head was DECLARED over the live channel this episode (hello/frame) AND this replay carried
   * the applied epoch up to it.
   *
   * SURFACES A STATUS (2b-2-a): the walk's hole is no longer discarded — it is returned so the
   * SHELL (2b-2-b) can schedule the retry ladder off a pure decision. `resync` NEVER sleeps; the
   * escalation across attempts is driven by the caller's `policy.deltasExhausted`, not by time.
   *
   * THE RETRY LADDER NEVER BLOCKS A READ: `validate()`'s awaited resync stays THIS bounded fast
   * pass; a read arriving during `transient-miss` classifies `unknown` and takes the posture
   * NOW — the ladder heals the future, not the present. Fail-closed means what it says. (So
   * `validate` calls `resync()` with the default policy and ignores the status; it does not wait
   * on retries, and a `transient-miss`/`needs-snapshot`/`terminal-gap` read fails closed at once
   * via the gate.)
   */
  async resync(policy?: ResyncPolicy): Promise<ResyncStatus> {
    // The tick path is use, and it runs inside a request context (the carrier fires it from a read).
    // Subscribing here is what makes a poll-driven isolate open its channel even before its first
    // read completes.
    this.ensureSubscribed();
    const deltasExhausted = policy?.deltasExhausted ?? false;
    const mirror = await this.#readMirrorPointer();
    if (!mirror) {
      // A pointer that has not propagated (stalled by chaos, tail) OR whose echoed `regId`
      // is not ours applies no prefix — an empty keyspace, fail closed. It may simply be
      // lagging, so signal a retry (seq 0 — the pointer/floor cursor is the "not yet visible"
      // thing). Convergence is still ATTEMPTED; it clears only on a declared head.
      this.#tryConverge();
      return transientMiss(0);
    }
    // β — apply the contiguous durable prefix from the floor `(0, lastBatch]`, advancing
    // `#appliedEpoch` toward the head a live-channel declaration named this episode. A pointer
    // parked at or behind the applied epoch — or one a lagging replica answered — restores no
    // warm trust on its own.
    const hole = await this.#replayBatches(
      mirror.store,
      mirror.keys,
      1,
      mirror.pointer.lastBatch
    );
    this.#tryConverge();
    if (hole === null) {
      return "converged";
    }
    // A hole at `hole`: the blessed delta is not yet visible. Retry it while the caller still
    // has delta-budget; once spent, escalate to the snapshot bridge (`resyncViaSnapshot`).
    return deltasExhausted ? "needs-snapshot" : transientMiss(hole);
  }

  /**
   * The SNAPSHOT bridge — the escalation the shell drives after `resync` returns
   * `needs-snapshot`. Load ONLY the pointer-blessed snapshot `__snap:<pointer.snapBatch>` (a
   * from-0 `BusFrame`, `fromEpoch:0`, parsed by the SAME `readFrame`), apply it as an ABSOLUTE
   * base (`#appliedEpoch` ← `snap.toEpoch` when ahead — bridging a hole in the now-GC-eligible
   * region below `snapBatch`), then replay the tail `(snapBatch, lastBatch]`. Never fetches an
   * UNBLESSED snap nor a delta past `lastBatch` (the over-fetch discipline the contract pins).
   *
   * A lagging `snapBatch` is FIRST-CLASS NORMAL (1c): `snapBatch` may trail `lastBatch`
   * arbitrarily — the arithmetic on `(snapBatch, lastBatch]` is correct, no warning, no anomaly.
   *
   * BLESSED-BUT-MISSING SNAP: if `__snap:<snapBatch>` is itself absent, give it ONE brief retry
   * (`transient-miss` — it may be lagging); once the caller's snapshot-budget is spent
   * (`snapshotExhausted`), rule permanently — a FULL delta replay from the floor if the chain
   * `(0, lastBatch]` is intact (⇒ `converged`), else `terminal-gap`. No spin, no serve-through.
   */
  async resyncViaSnapshot(policy?: SnapshotPolicy): Promise<ResyncStatus> {
    this.ensureSubscribed();
    const snapshotExhausted = policy?.snapshotExhausted ?? false;
    const mirror = await this.#readMirrorPointer();
    if (!mirror) {
      this.#tryConverge();
      return transientMiss(0);
    }
    const { store, keys, pointer } = mirror;
    const snapRaw = await store.get(keys.snapKey(pointer.snapBatch), "snap");
    const snap = snapRaw ? readFrame(snapRaw.value) : undefined;
    if (!snap) {
      // Blessed-but-missing snapshot. Brief retry first; then full-replay-if-intact else terminal.
      if (!snapshotExhausted) {
        this.#tryConverge();
        return transientMiss(pointer.snapBatch);
      }
      // FULL delta replay from the floor `(0, lastBatch]` — no snap to bridge below.
      const holeNoSnap = await this.#replayBatches(
        store,
        keys,
        1,
        pointer.lastBatch
      );
      this.#tryConverge();
      return holeNoSnap === null ? "converged" : "terminal-gap";
    }
    // Apply the snapshot as an absolute base (jumps the applied epoch over a below-snapBatch
    // hole), then replay ONLY the tail `(snapBatch, lastBatch]` — the deltas below the floor are
    // GC-eligible (the snap superseded them), so their absence is NOT a hole. Starting the
    // walk at `snapBatch + 1` is what makes a lagging `snapBatch` first-class normal.
    this.#applySnapshotBase(snap);
    const tailHole = await this.#replayBatches(
      store,
      keys,
      pointer.snapBatch + 1,
      pointer.lastBatch
    );
    this.#tryConverge();
    if (tailHole === null) {
      return "converged";
    }
    // A hole ABOVE the snapshot floor is a recent tail delta not yet visible — retry (we HAVE a
    // floor, so this is transient, never terminal; the terminal discriminator needs the snap GONE).
    return transientMiss(tailHole);
  }

  /**
   * Read the `__state` pointer through the registry-scoped keys, returning the scoped
   * `store`/`keys` alongside it, or `undefined` when the pointer is absent/malformed OR echoes a
   * foreign `regId` (a scheme/prefix divergence or another registry's pointer — fail closed).
   */
  async #readMirrorPointer(): Promise<
    { store: Store; keys: MirrorKeys; pointer: StatePointer } | undefined
  > {
    const store = this.#stateStore;
    const keys = mirrorKeys(this.#regId);
    const raw = await store.get(keys.stateKey, "pointer");
    const pointer = raw ? readPointer(raw.value) : undefined;
    if (!pointer || pointer.regId !== this.#regId) {
      return undefined;
    }
    return { keys, pointer, store };
  }

  /**
   * Replay batches `fromSeq..lastBatch` (XRANGE discipline), applying each whose
   * `fromEpoch` is contiguous with the applied epoch. Returns the seq of the FIRST hole
   * (a missing/garbled/non-contiguous forward batch — fail closed), or `null` when the walk
   * reached `lastBatch` hole-free. `fromSeq` is `1` for a from-floor `(0, lastBatch]` replay and
   * `snapBatch + 1` for a snapshot-bridged tail `(snapBatch, lastBatch]` — so GC'd batches below
   * a snapshot floor are never miscounted as holes. Reads ONLY `deltaKey(fromSeq..lastBatch)` —
   * never a seq past the pointer — through the SAME `keys` the pointer was read with (
   * over-fetch discipline).
   */
  async #replayBatches(
    store: Store,
    keys: MirrorKeys,
    fromSeq: number,
    lastBatch: number
  ): Promise<number | null> {
    let firstHole: number | null = null;
    for (let seq = fromSeq; seq <= lastBatch; seq++) {
      // Batches replay in seq order (XRANGE); a serial walk is the honest cost
      // of a cold resync against a per-key-visibility store.
      const batchRaw = await store.get(keys.deltaKey(seq), "delta");
      const frame = batchRaw ? readFrame(batchRaw.value) : undefined;
      const applied = frame ? this.#applyIfContiguous(frame) : false;
      if (!(frame && applied) && firstHole === null) {
        // A batch not yet visible, garbled, or non-contiguous is a hole: affected knowledge
        // stays unknown (fail closed). Record the FIRST such seq (the frontier the retry ladder
        // targets); keep walking so an already-applied later batch is not miscounted as a hole.
        firstHole = seq;
      }
    }
    return firstHole;
  }

  /** Apply a batch frame iff it advances the applied epoch contiguously. */
  #applyIfContiguous(frame: BusFrame): boolean {
    if (frame.toEpoch <= this.#appliedEpoch) {
      return true; // already applied — not a hole
    }
    if (frame.fromEpoch !== this.#appliedEpoch) {
      return false; // a genuine gap — cannot advance past the hole
    }
    applyChanges(this.#known, frame);
    this.#advanceApplied(frame.toEpoch);
    return true;
  }

  /**
   * Apply a from-0 snapshot frame as an ABSOLUTE base: merge its watermarks (max-on-write,
   * never regressing a higher already-known value) and JUMP the applied epoch to `snap.toEpoch`
   * when the snapshot is AHEAD — a complete `(0, snap.toEpoch]` checkpoint carries its own
   * contiguity from the floor, so no `fromEpoch === applied` gate applies. A snapshot BEHIND the
   * applied epoch (a lagging `snapBatch` — first-class normal) adds nothing and never regresses.
   */
  #applySnapshotBase(snap: BusFrame): void {
    if (snap.toEpoch <= this.#appliedEpoch) {
      return; // lagging snapBatch — already past this checkpoint; nothing to bridge below.
    }
    applyChanges(this.#known, snap);
    this.#advanceApplied(snap.toEpoch);
  }

  /**
   * derivative suspicion RAISING — a PURE state update fed one pointer
   * observation. A heartbeat refreshes the pointer's freshness field without minting epochs, so
   * freshness MOTION is not evidence the reader has caught up; only a live-channel head declaration
   * anchors convergence. This monitor uses freshness the OTHER way — to RAISE: it tracks OBSERVED
   * SILENCE (how long `flushedAt` has failed to advance) and raises `#suspect` once that silence
   * reaches `#suspicionAfterMs`. DERIVATIVE-ONLY, RAISE-ONLY: `flushedAt` never clears suspicion nor
   * anchors absolute freshness — only `>`/equality ACROSS observations, only to RAISE.
   * `nowMs` is compared ONLY to our own prior watch-times (`#lastObservationMs`, `#silenceStartMs`),
   * NEVER to `flushedAt`, so no absolute-age anchor sneaks in. Measuring ELAPSED silence (not
   * counting non-advancing ticks) makes the raise SAMPLING-RATE-INDEPENDENT: fast ticks cannot
   * witness `#suspicionAfterMs` of silence in less time, and a slow-but-live writer resets the
   * window. The shell feeds it observations (2b-2-b); here explicit test ticks.
   */
  notePointerObservation(pointer: StatePointer, nowMs: number): void {
    // Guard BEFORE any write: a stale/reordered observation (`nowMs` not strictly after the last)
    // is a true no-op, so it can never corrupt the baseline (`#lastFlushedAt`/`#silenceStartMs`).
    if (this.#lastObservationMs !== null && nowMs <= this.#lastObservationMs) {
      return;
    }
    this.#lastObservationMs = nowMs;
    if (this.#lastFlushedAt === null || this.#silenceStartMs === null) {
      // First observation — seed the baseline; silence starts now. Nothing to judge yet.
      this.#lastFlushedAt = pointer.flushedAt;
      this.#silenceStartMs = nowMs;
      return;
    }
    if (pointer.flushedAt > this.#lastFlushedAt) {
      // `flushedAt` advanced — the writer is alive; the silence window resets to now.
      this.#lastFlushedAt = pointer.flushedAt;
      this.#silenceStartMs = nowMs;
      return;
    }
    // `flushedAt` did not advance — silence continues from `#silenceStartMs`. Raise once the
    // OBSERVED silence reaches the threshold (raise-only; `#tryConverge` alone ever clears).
    if (nowMs - this.#silenceStartMs >= this.#suspicionAfterMs) {
      this.#suspect = true;
    }
  }

  /**
   * The poll DECISION — a PURE, deterministic predicate: has at least `thresholdMs`
   * elapsed since the last poll? The base cadence is maxSyncLag (60s) and the ±20% jitter is the
   * SHELL's rng job (2b-2-b) — both enter as the caller-computed `thresholdMs`, so no ambient
   * time/rng lives here. The poll DRIVER (the timer that calls this) is 2b-2-b.
   */
  pollDue(nowMs: number, lastPollMs: number, thresholdMs: number): boolean {
    return nowMs - lastPollMs >= thresholdMs;
  }

  /**
   * The `bornEpoch` to stamp into a fresh envelope at fill start, read
   * SYNCHRONOUSLY from LOCAL VERIFIED knowledge — the highest epoch this isolate has
   * observed from any authority (contiguous bus applies OR a recorded live check),
   * never a fresh live read. So the default read path keeps the zero-round-trip cost
   * model, while a fill that follows a verified live check postdates what that check
   * observed — a legitimate refill on a behind isolate is not fenced by its own
   * recorded watermark. A lagging high-water can only UNDER-state bornEpoch ⇒
   * over-invalidation ⇒ the safe direction. The caller snapshots
   * this EARLY as the fence-critical floor (a concurrent bus purge landing after the
   * snapshot cannot lift it, so INV-9/12 hold) and only raises the fill's bornEpoch by
   * the epoch its OWN reads verified (via `validate`'s `checkedThrough`), never by a
   * concurrent frame — see `runGetOrSet`.
   */
  localBornEpoch(): number {
    return this.#knownThroughEpoch;
  }

  /**
   * The verdict `explain` reports — a PURE read of CURRENT local knowledge: classify
   * the envelope against what this isolate knows NOW, with NO live check, NO resync, and NO
   * recording. It is exposition, not testimony: it adds no tracked state and mutates nothing (the
   * introspection-not-testimony line). A suspect map fails closed to `unknown` (the posture
   * an eventual read would take), and `unknown` stays `unknown`. The soft/hard watermarks are the
   * evidence the bornEpoch was weighed against — the purge epochs behind a `stale`/`dead` verdict
   * (`0` when nothing is known / fail-closed).
   */
  explainVerdict(envelope: Envelope): {
    readonly validity: Validity;
    readonly softEpoch: number;
    readonly hardEpoch: number;
  } {
    const knowledge = this.#localKnowledge(envelope.tags);
    let softEpoch = 0;
    let hardEpoch = 0;
    for (const k of knowledge) {
      if (k.known) {
        softEpoch = Math.max(softEpoch, k.softEpoch);
        hardEpoch = Math.max(hardEpoch, k.hardEpoch);
      }
    }
    const validity: Validity = this.#suspect
      ? "unknown"
      : classify(envelope.bornEpoch, knowledge);
    return { hardEpoch, softEpoch, validity };
  }

  /**
   * Validate an envelope against this isolate's knowledge (table). `strong`
   * (or an unknown tag under `registry-check`) consults a live registry read; a
   * plain `eventual` read uses only local bus-fed knowledge and fails closed on any
   * unknown/suspect tag (invariant 11). Never serves a hard-purged value.
   *
   * REACHABILITY IS SPLIT BY CHANNEL. This method is the REGISTRY-RPC half: a failure here is
   * a strong read's `RegistryUnavailableError` or, on the eventual path, a `'miss'`.
   * The BUS half is not represented here at all — a bus that is down or never came up is never
   * grounds for a throw, because the eventual path's whole design is to be correct without it.
   */
  async validate(
    envelope: Envelope,
    consistency: Consistency,
    resolution: UnknownResolution,
    onUnavailable?: "eventual"
  ): Promise<ValidationResult> {
    const { tags, bornEpoch } = envelope;
    // The first read that needs the channel opens it — inside THIS request's context, which is
    // the only place a dial is legal. Idempotent; the dial completes on its own time.
    this.ensureSubscribed();

    if (consistency === "strong") {
      // A strong read observes every durably-accepted purge: one live, un-memoized
      // registry read is the authority — local knowledge is not consulted.
      try {
        return await this.#strongLiveCheck(tags, bornEpoch);
      } catch (error: unknown) {
        // the letter, implemented: an unreachable registry means this read CANNOT make the promise
        // `strong` is defined by, so it says so rather than quietly answering with a weaker one.
        if (onUnavailable !== "eventual") {
          throw new RegistryUnavailableError(
            `strong read could not reach the registry — ${String(error)}`
          );
        }
        // The caller opted out and the reachability rule defines what the opt-out MEANS: this call degrades to
        // the eventual posture — local knowledge, fail-closed on anything unknown — for THIS call
        // only. The instance's consistency is unchanged; nothing is remembered.
        //
        // AND IT IS ANNOUNCED. A degrade the caller asked for is still a degrade an operator has to
        // be able to see. A strong read that quietly returns eventual's guarantee has misreported its
        // own health, and it has done so to the caller who chose `strong` precisely because the
        // answer had to be right. The payload is a reason CLASS — never the registry's own error text.
        this.#emit?.({
          reason: STRONG_DEGRADED_REGISTRY_UNREACHABLE,
          type: TELEMETRY_EVENTS.strong_degraded,
        });
      }
    }

    // Eventual: consult local knowledge. A suspect map (unresolved gap/reconnect) may be
    // missing a durably-accepted purge, so a resync is attempted first.
    //
    // THE RETRY LADDER NEVER BLOCKS A READ: this awaited resync is the bounded fast pass and its
    // returned status is intentionally NOT awaited-upon for retries — a read arriving during
    // `transient-miss` classifies `unknown` and takes the posture NOW (the gate
    // below), the ladder heals the future not the present. Fail-closed means what it says. The
    // shell (2b-2-b) is what re-drives `resync`/`resyncViaSnapshot` off that status out of band.
    if (this.#suspect) {
      await this.resync();
    }
    // THE GATE: while the map is STILL suspect (the resync did not converge),
    // every tag is treated as unknown and fails closed. A map that may have skipped a
    // durable purge is not the merely-degraded warm map Q4 lets an isolate continue on;
    // so local knowledge, however complete it looks, is not consulted until convergence.
    const localVerdict: Validity = this.#suspect
      ? "unknown"
      : classify(bornEpoch, this.#localKnowledge(tags));
    if (localVerdict !== "unknown") {
      return { checkedThrough: 0, validity: localVerdict };
    }

    // ── UNKNOWN AT EVENTUAL: ORIGIN 1 — the map's own gap/absence ──
    // Resolved per `unknownPolicy`, exactly as unknown resolution rules. 'miss' and 'error' never consult the
    // registry (INV-6); 'registry-check' does one live read.
    if (typeof resolution !== "string") {
      // the 'error' — and what separates it from every other resolution is what it DOES: the
      // read THROWS rather than refilling. This is the
      // takedown/embargo posture — the one case where "origin is truth" does not hold, so refilling
      // could resurrect exactly the content that had to disappear. Resolving this to a miss (as a
      // byte-identical 'error' and 'miss' would) is not a smaller failure than throwing; it is the
      // hazard the policy was added to prevent, in the namespace least able to afford it.
      //
      // The payload is EXACTLY the user tags this read could not establish — the question the
      // caller is being asked to answer is "which dependency is embargoed?", and every tag the
      // entry happens to declare is not an answer to it.
      throw new UnknownTagError(
        envelope.key,
        this.#unknownUserTags(tags),
        resolution.armedBy
      );
    }
    if (resolution !== "registry-check") {
      return { checkedThrough: 0, validity: "unknown" };
    }
    let checked: readonly TagKnowledge[];
    try {
      checked = await this.#registry.check(tags);
    } catch {
      // ── ORIGIN 2 — the CHECK ITSELF failed (registry-down row) ──
      // TWO ORIGINS, TWO RULES. This unknown is NOT the suspect map's unknown above, and it must not
      // be re-routed through `unknownPolicy`: that policy's own default IS the check that just
      // failed, so consulting it here would either loop or answer with the very mechanism that is
      // down. Unknown resolution covers this case directly — treat dead, refill; origin is truth — and the
      // registry-down row says the same in the other direction: unknown ⇒ miss, NEVER stale.
      // Hard-safe and self-healing, and it is the reason a registry outage costs origin load rather
      // than correctness.
      //
      // (A `takedownSensitive` namespace never arrives here: its resolution is 'error', which
      // returns above without ever reaching a check — the registry-unreachable clause, honored by
      // the override rather than by a second rule in this catch.)
      return { checkedThrough: 0, validity: "unknown" };
    }
    // The live check is verified authority: record its knowledge into `#known` so the
    // tag is no longer unknown. This (a) makes a born-dead value's first eventual read
    // resolve DEAD → refill, and (b) preserves eventual consistency — subsequent eventual reads of this
    // now-known tag are zero-round-trip; only the FIRST unknown read pays the check,
    // which unknown resolution permits (an unknown tag fails closed and pays a registry check).
    this.#recordChecked(tags, checked);
    return {
      checkedThrough: throughEpochOf(checked),
      validity: classify(bornEpoch, checked),
    };
  }

  /**
   * The strong-read live check: ONE live, un-memoized registry read over
   * `tags`, memoized into local knowledge (`#recordChecked`), classified against
   * `bornEpoch` via the table. The single engine of both the strong READ arm of
   * `validate` and the pre-fetch strong-miss check (`liveCheckForFill`) —
   * local knowledge is never consulted here; the primary read is the authority.
   */
  async #strongLiveCheck(
    tags: readonly string[],
    bornEpoch: number
  ): Promise<ValidationResult> {
    const knowledge = await this.#registry.check(tags);
    this.#recordChecked(tags, knowledge);
    return {
      checkedThrough: throughEpochOf(knowledge),
      validity: classify(bornEpoch, knowledge),
    };
  }

  /**
   * Pre-fetch strong-miss live check: the SAME live un-memoized
   * registry read `#strongLiveCheck` runs, executed BEFORE the factory on a strong fill so
   * the fill's bornEpoch is minted from now-current knowledge — an understatement, never an
   * overstatement, of recency (the safe direction). Records the reading into `#known` (
   * memoization), so a later eventual read of the tag fails closed with no second round-trip.
   * Returns the verified high-water; the caller raises the fill's bornEpoch to it.
   */
  async liveCheckForFill(
    tags: readonly string[],
    onUnavailable?: "eventual"
  ): Promise<number> {
    try {
      const { checkedThrough } = await this.#strongLiveCheck(tags, 0);
      return checkedThrough;
    } catch (error: unknown) {
      // REACHABILITY — the SECOND registry-RPC site on the strong path, and it obeys the
      // same rule as the first for the same reason: this is part of a strong read, so an unreachable
      // registry means the read cannot make strong's promise. Leaving it unguarded would let a
      // caller who opted OUT of strong-throws still get a raw, untyped throw from the fill — the
      // opt-out honored on one path and ignored on the other.
      if (onUnavailable !== "eventual") {
        throw new RegistryUnavailableError(
          `strong fill could not reach the registry to mint its bornEpoch — ${String(error)}`
        );
      }
      this.#emit?.({
        reason: STRONG_DEGRADED_REGISTRY_UNREACHABLE,
        type: TELEMETRY_EVENTS.strong_degraded,
      });
      // Degrade to the eventual posture: mint from LOCAL knowledge. `0` adds nothing to the caller's
      // `Math.max` floor, which is already `localBornEpoch()` — so the fill is born at what this
      // isolate has actually verified. An understatement of recency, never an overstatement (the
      // safe direction): a born epoch that is too LOW over-invalidates, which costs a refill; one
      // that is too high would serve a value against a purge it never saw.
      return 0;
    }
  }

  /** Merge a verified live-check result into local knowledge. Max-on-write, so
   * a concurrently-applied newer bus frame is never regressed. The check's
   * `throughEpoch` advances the verified high-water (bornEpoch source) so a subsequent
   * refill postdates what this check observed. */
  #recordChecked(
    tags: readonly string[],
    knowledge: readonly TagKnowledge[]
  ): void {
    for (const [i, tag] of tags.entries()) {
      const k = knowledge[i];
      if (k === undefined || !k.known) {
        continue;
      }
      const w = this.#known.get(tag) ?? { hardEpoch: 0, softEpoch: 0 };
      w.softEpoch = Math.max(w.softEpoch, k.softEpoch);
      w.hardEpoch = Math.max(w.hardEpoch, k.hardEpoch);
      this.#known.set(tag, w);
      this.#knownThroughEpoch = Math.max(
        this.#knownThroughEpoch,
        k.throughEpoch
      );
    }
  }

  /**
   * The write-back fence, evaluated against LOCAL bus-applied knowledge: any tag
   * `hardEpoch > bornEpoch` in the isolate's contiguously-applied state ⇒ the candidate
   * is fenced (a hard purge landed DURING the fill and was delivered here). Returns
   * `true` when the write-back may proceed. A soft purge lands already-stale but still
   * stores; only DEAD fences.
   *
   * DESIGN (per the INV-12 erratum): the write-back guard is realized as a LOCAL
   * fence here plus read-time fail-closed resolution — not a live registry read on the
   * write path. The local fence catches a purge that landed AND delivered locally DURING the
   * fill (tag KNOWN-dead locally ⇒ INV-9/12); it lets a cold/dropped-frame isolate STORE a value
   * whose tag is locally unknown (origin is truth after a purge — the legitimate
   * refill case). A born-dead value that slips past this fence is stored but INERT: its own
   * SUBSEQUENT eventual reads fail closed on the unknown tag and live-check — that read-time
   * check is where the GOAL (never serve a value born against a durably-accepted purge) is
   * enforced for THAT path. A strong fill's OWN serve is anchored earlier, at INITIATION: the
   * pre-factory check minted its `bornEpoch` at the global epoch, so it already dominates
   * every purge accepted before initiation — no read-time check re-verifies the fill
   * that produced it.
   */
  fenceWriteBack(tags: readonly string[], bornEpoch: number): boolean {
    return classify(bornEpoch, this.#localKnowledge(tags)) !== "dead";
  }

  /** Local tri-state knowledge for these tags (absent ⇒ unknown). */
  /**
   * The USER tags this read could not establish — what `UnknownTagError` carries.
   *
   * Under suspicion EVERY tag is unknown (the gate treats a map with a hole as knowing
   * nothing), so the whole user set is the honest answer there. Otherwise it is exactly the tags the
   * map has no watermark for. Reserved tags are dropped either way: `__k:`/`__ns:` are the kernel's
   * own bookkeeping, they are unknown for the same reason the user's tags are, and a caller cannot
   * do anything with them — the same reason they never reach an L3 header.
   */
  #unknownUserTags(tags: readonly string[]): readonly string[] {
    const knowledge = this.#localKnowledge(tags);
    return tags.filter(
      (tag, index) =>
        !isReservedTag(tag) &&
        (this.#suspect || knowledge[index]?.known !== true)
    );
  }

  #localKnowledge(tags: readonly string[]): readonly TagKnowledge[] {
    return tags.map((tag): TagKnowledge => {
      const w = this.#known.get(tag);
      if (!w) {
        return { known: false };
      }
      return {
        hardEpoch: w.hardEpoch,
        known: true,
        softEpoch: w.softEpoch,
        throughEpoch: this.#appliedEpoch,
      };
    });
  }
}
