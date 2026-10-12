// The pure registry state machine (Coordinator core, thin-shell foundation).
//
// A FUNCTIONAL CORE for the authoritative registry (spec §4.1/§4.2/§4.4/§4.5,
// errata E11/E12): commands in, {state transition + effect DESCRIPTIONS} out. It
// performs NO I/O, holds NO callbacks, contains NO promises, and reads NO ambient
// clock. Two hosts embed this one machine — the harness FakeRegistry (Sim bus/store)
// and, later, a Cloudflare Durable Object (storage/alarms/sockets) — so the same
// command script yields the same responses, the same emitted effects, and the same
// content-addressed batch bytes on both.
//
//   - It does not persist   -> it returns a persist-batch effect (the delta batch), and, on
//                              the snapshot cadence, a persist-snapshot effect (the F12
//                              canonical batch over (0, epoch]).
//   - It does not broadcast  -> it returns a broadcast-frame effect (the bus frame).
//   - It does not bless      -> it returns ONE terminal advance-pointer effect per flush (the
//                              `__state` pointer), ordered after every immutable-object effect,
//                              so a host issues exactly one `__state` put per flush (§4.2, D17).
//   - It does not schedule    -> it returns a schedule-flush effect (a flush deadline).
//
// The HOSTS execute those effect descriptions against their own plumbing. Time enters
// ONLY as parameters (flush/gc take nowMs) feeding the non-correctness-bearing inputs;
// the epoch counter is the correctness clock and is minted as machine STATE, never
// from wall time.

import type { BusFrame, RegistryAck, TagChange } from "../drivers";
import type { TagKnowledge } from "../knowledge";
import { assertRegId, DEFAULT_REG_ID } from "./mirror-keys";
import type { StatePointer } from "./mirror-keys";

/** The two watermark axes: soft (expire) and hard (delete) — §4.1. */
export type PurgeKind = "soft" | "hard";

/** A tag's dual watermark — independent, monotone, max-on-write (§4.1). */
export interface WatermarkView {
  readonly softEpoch: number;
  readonly hardEpoch: number;
}

/** Broadcast one replication frame on the invalidation bus (§4.2). */
export interface BroadcastFrameEffect {
  readonly kind: "broadcast-frame";
  readonly frame: BusFrame;
}

/**
 * Persist one content-addressed delta batch (§4.2, E12). `seq` is the durable batch number
 * (`lastBatch + 1`); the batch is addressed by the frame's `(fromEpoch, toEpoch)` range, so a
 * replay of the same range is byte-identical. An IMMUTABLE, write-once object; the `__state`
 * pointer that blesses it is the flush's single terminal `advance-pointer` effect.
 */
export interface PersistBatchEffect {
  readonly kind: "persist-batch";
  readonly seq: number;
  readonly frame: BusFrame;
}

/* References: §4.2. */

export interface PersistSnapshotEffect {
  readonly kind: "persist-snapshot";
  readonly seq: number;
  readonly frame: BusFrame;
}

/**
 * Telemetry: a DUE snapshot was SKIPPED because its canonical shipped bytes exceed the configured
 * ceiling (§4.2, CACHE-23). NOT a persisted object — a pure observability effect the flush emits
 * INSTEAD of `persist-snapshot`: `#snapBatch` does NOT advance (no phantom checkpoint is blessed),
 * while `persist-batch`, `broadcast-frame`, and the terminal `advance-pointer` proceed NORMALLY
 * (the pointer reflects the UNCHANGED `snapBatch`). `bytes` is the measured UTF-8 length of the
 * exact serialization that would have shipped; `ceiling` is the effective `maxSnapshotBytes`. The
 * skip is LOUD by design: the batch clause stays tripped, so every subsequent flush re-attempts.
 */
export interface SnapshotSkippedEffect {
  readonly kind: "telemetry";
  readonly event: "snapshot-skipped-oversize";
  readonly bytes: number;
  readonly ceiling: number;
}

/* References: §4.2. */

export interface AdvancePointerEffect {
  readonly kind: "advance-pointer";
  readonly pointer: StatePointer;
}

/**
 * Schedule the next flush no later than `deadlineMs` (§4.2). The host owns the timer:
 * a coalescing DO arms an alarm; the fake host flushes synchronously (window 0), so a
 * due deadline means flush now. The ONLY effect a mutation emits.
 */
export interface ScheduleFlushEffect {
  readonly kind: "schedule-flush";
  readonly deadlineMs: number;
}

/**
 * The effects a FLUSH emits — the immutable-object writes, the broadcast, and the ONE terminal
 * pointer advance. Never a `schedule-flush` (that is a mutation effect). `executeFlushEffects`
 * (src/registry/execute.ts) drives exactly this set, so the write order + once-only `__state`
 * bless live in one place across both hosts (the thin-shell rule on the write path).
 */
export type FlushEffect =
  | AdvancePointerEffect
  | BroadcastFrameEffect
  | PersistBatchEffect
  | PersistSnapshotEffect
  | SnapshotSkippedEffect;

/** The effect descriptions a host executes after a machine transition. */
export type RegistryEffect = FlushEffect | ScheduleFlushEffect;

/** Default snapshot cadence (§4.2): checkpoint every 500 batches OR every 6h. */
const DEFAULT_SNAPSHOT_EVERY_BATCHES = 500;
const DEFAULT_SNAPSHOT_INTERVAL_MS = 6 * 60 * 60 * 1000;
/** Default snapshot SIZE ceiling (§4.2, CACHE-23): the KV 25 MiB per-value profile. A due
 * snapshot whose canonical shipped bytes exceed this is SKIPPED (never truncated, never a phantom
 * bless) — the machine-owned gate above the executor-side delta bound. */
const DEFAULT_MAX_SNAPSHOT_BYTES = 25 * 1024 * 1024;

/* References: §4.2, E19. */

export interface RegistryMachineOptions {
  /** Emit a snapshot once `seq − snapBatch` reaches this many batches (replay-safe). */
  readonly snapshotEveryBatches?: number;
  /** Emit a snapshot once `nowMs − lastSnapMs` reaches this many ms (replay via recorded nowMs). */
  readonly snapshotIntervalMs?: number;
  /** Skip a due snapshot whose canonical shipped bytes exceed this ceiling (§4.2, CACHE-23).
   * Journaled like the cadence, so the skip is replay-deterministic. Unset ⇒ the 25 MiB default. */
  readonly maxSnapshotBytes?: number;
  /** The registry id scoping the §4.2 mirror keyspace (E19). Unset ⇒ `"default"`. Validated
   * `[a-z0-9._-]`, ≤ 64 — a malformed id fails loud here, never mints an ambiguous key. */
  readonly regId?: string;
}

/* References: §4.2, E19. */

export interface EffectiveConfig {
  readonly snapshotEveryBatches: number;
  readonly snapshotIntervalMs: number;
  readonly maxSnapshotBytes: number;
  readonly regId: string;
}

/** A durably-accepted mutation: the epoch ack plus the flush the host must schedule. */
export interface MutationTransition {
  readonly ack: RegistryAck;
  readonly effects: readonly ScheduleFlushEffect[];
}

/**
 * A GC horizon description (§4.5, E11). Watermarks whose era predates `collectBeforeMs`
 * are collectible; `horizonMs` is the monotone high-water retention. A pure computation
 * over machine state and the supplied `nowMs` — the machine collects nothing itself.
 */
export interface GcHorizon {
  readonly horizonMs: number;
  readonly collectBeforeMs: number;
}

interface MutableWatermark {
  softEpoch: number;
  hardEpoch: number;
}

/** A per-tag axis accumulator while folding a batch window into canonical form (§4.2). */
interface TagDelta {
  softEpoch?: number;
  hardEpoch?: number;
}

/**
 * Fold a coalescing window's buffered changes into the CANONICAL batch form (§4.2, E12):
 *   - every tag appears EXACTLY ONCE, carrying its max soft/hard epoch over the window
 *     (any duplicate tags folded via max-on-write), and
 *   - the changes are ordered LEXICOGRAPHICALLY by tag (UTF-16 code-unit order).
 * Only an axis actually touched in the window is emitted — an untouched axis has no delta
 * over the (fromEpoch, toEpoch] range. Content-addressing (E12) makes a batch a pure
 * function of durable state over its range, so it has one serialization; the kernel applies
 * changes via max-on-write, which is commutative and idempotent (§4.1), so dedup + lex-sort
 * carry the SAME knowledge as the raw concatenation (INV-14/19/20 preserved). The emitted
 * key order is alphabetical (hardEpoch, softEpoch, tag) — the repo's stable wire convention —
 * so a canonical change has exactly one serialization on every host.
 */
function canonicalBatch(pending: readonly TagChange[]): TagChange[] {
  const folded = new Map<string, TagDelta>();
  for (const change of pending) {
    const acc = folded.get(change.tag) ?? {};
    if (change.softEpoch !== undefined) {
      acc.softEpoch = Math.max(
        acc.softEpoch ?? change.softEpoch,
        change.softEpoch
      );
    }
    if (change.hardEpoch !== undefined) {
      acc.hardEpoch = Math.max(
        acc.hardEpoch ?? change.hardEpoch,
        change.hardEpoch
      );
    }
    folded.set(change.tag, acc);
  }
  return [...folded.keys()].toSorted().map((tag): TagChange => {
    const acc = folded.get(tag) ?? {};
    return {
      ...(acc.hardEpoch === undefined ? {} : { hardEpoch: acc.hardEpoch }),
      ...(acc.softEpoch === undefined ? {} : { softEpoch: acc.softEpoch }),
      tag,
    };
  });
}

/**
 * The registry state machine. Holds dual watermark epochs per tag, a monotone epoch
 * counter, a durable batch sequence, the monotone GC-horizon high-water, the snapshot
 * cadence watermark (`snapBatch`/`lastSnapMs`), and the un-flushed (coalescing) change
 * buffer. Every method is synchronous and side-effect free with respect to the outside
 * world: mutations update machine state and return effect descriptions; queries return
 * values.
 */
export class RegistryMachine {
  readonly #watermarks = new Map<string, MutableWatermark>();
  #epoch = 0;
  #batchSeq = 0;
  #gcHorizonMs = 0;
  // The coalescing window's un-flushed changes. The fake host flushes after every
  // mutation, so this holds a single mutation's changes; a DO host accumulates many
  // across an alarm interval and mints one wider-range batch (E12).
  #pendingFrom: number | null = null;
  #pendingChanges: TagChange[] = [];
  // Snapshot cadence (§4.2). `#snapBatch` is the batch seq of the last SHIPPED snapshot (last
  // SUCCESS — mirrored into `__state.snapBatch`, advanced only on a persist). `#lastSnapMs` is the
  // `nowMs` baseline for the time clause and means "last ATTEMPT," NOT last success: it advances on
  // every due snapshot INCLUDING an oversize SKIP (CACHE-23), so a skip quiets the time clause while
  // the batch clause (`#snapBatch`, unadvanced) stays tripped and loud. Null until the first flush,
  // so the very first batch never trips the time clause.
  #snapBatch = 0;
  #lastSnapMs: number | null = null;

  #snapshotEveryBatches = DEFAULT_SNAPSHOT_EVERY_BATCHES;
  #snapshotIntervalMs = DEFAULT_SNAPSHOT_INTERVAL_MS;
  // The effective snapshot SIZE ceiling (§4.2, CACHE-23) — machine STATE, seeded/re-set via
  // `configure` exactly like the cadence, so the oversize skip is replay-deterministic.
  #maxSnapshotBytes = DEFAULT_MAX_SNAPSHOT_BYTES;
  /* References: E19. */

  #regId = DEFAULT_REG_ID;

  constructor(options: RegistryMachineOptions = {}) {
    this.configure(options);
  }

  /** The current Coordinator epoch (the correctness clock, machine state). */
  get currentEpoch(): number {
    return this.#epoch;
  }

  /** The batch seq of the last emitted snapshot (§4.2 `__state.snapBatch`), machine state. */
  get snapBatch(): number {
    return this.#snapBatch;
  }

  /** The last durably-emitted batch seq (§4.2 `lastBatch`), machine state — both the flush and the E5 heartbeat pointer effects carry it; a host may also read it directly. */
  get lastBatch(): number {
    return this.#batchSeq;
  }

  /* References: §4.2, E19. */
  get effectiveConfig(): EffectiveConfig {
    return {
      maxSnapshotBytes: this.#maxSnapshotBytes,
      regId: this.#regId,
      snapshotEveryBatches: this.#snapshotEveryBatches,
      snapshotIntervalMs: this.#snapshotIntervalMs,
    };
  }

  /** The mirror-key scoping id (§4.2, E19), machine state — the host reads it to build the
   * scoped keyspace it writes the flush effects to (`mirrorKeys(regId)`). */
  get regId(): string {
    return this.#regId;
  }

  /* References: §4.2. */

  configure(options: RegistryMachineOptions): void {
    this.#snapshotEveryBatches =
      options.snapshotEveryBatches ?? DEFAULT_SNAPSHOT_EVERY_BATCHES;
    this.#snapshotIntervalMs =
      options.snapshotIntervalMs ?? DEFAULT_SNAPSHOT_INTERVAL_MS;
    this.#maxSnapshotBytes =
      options.maxSnapshotBytes ?? DEFAULT_MAX_SNAPSHOT_BYTES;
    // E19: `regId` is validated at configure time — the charset gate that makes the scoped
    // keyspace injective (no `:` inside an id). Unset falls to `"default"`, exactly as the
    // constructor seed does, so a cadence-only reconfigure that omits it re-seeds the default.
    const regId = options.regId ?? DEFAULT_REG_ID;
    assertRegId(regId);
    this.#regId = regId;
  }

  /** The monotone high-water GC horizon in ms — never retracts (§4.5, E11, INV-28). */
  get gcHorizonMs(): number {
    return this.#gcHorizonMs;
  }

  /** Authoritative dual watermark for a tag; absent tags read as a zero watermark. */
  watermarkOf(tag: string): WatermarkView {
    const w = this.#watermarks.get(tag);
    return { hardEpoch: w?.hardEpoch ?? 0, softEpoch: w?.softEpoch ?? 0 };
  }

  /**
   * Live watermark knowledge for these tags (E1). A primary read is always authoritative
   * and known; an untouched tag reads as a known zero watermark, never as unknown (§4.4).
   */
  knowledgeOf(tags: readonly string[]): readonly TagKnowledge[] {
    const through = this.#epoch;
    return tags.map((tag): TagKnowledge => {
      const w = this.#watermarks.get(tag);
      return {
        hardEpoch: w?.hardEpoch ?? 0,
        known: true,
        softEpoch: w?.softEpoch ?? 0,
        throughEpoch: through,
      };
    });
  }

  /** Soft purge: max-on-write bump of `softEpoch` for each tag (§2.2, §4.1). */
  expire(tags: readonly string[], nowMs: number): MutationTransition {
    return this.#mutate(tags, "soft", nowMs);
  }

  /** Hard purge: max-on-write bump of `hardEpoch` for each tag (§2.2, §4.1). */
  delete(tags: readonly string[], nowMs: number): MutationTransition {
    return this.#mutate(tags, "hard", nowMs);
  }

  /** Register a retention lease; the GC horizon rises to the high-water (§4.5, E11). */
  registerRetention(retentionMs: number): void {
    this.#gcHorizonMs = Math.max(this.#gcHorizonMs, retentionMs);
  }

  /**
   * Describe the GC horizon at `nowMs` (§4.5, E11). Pure: the machine collects nothing;
   * a host uses `collectBeforeMs` to decide what to reclaim. Uncertainty extends, never
   * shortens, the horizon — it is the monotone high-water retention.
   */
  gc(nowMs: number): GcHorizon {
    return {
      collectBeforeMs: nowMs - this.#gcHorizonMs,
      horizonMs: this.#gcHorizonMs,
    };
  }

  /**
   * Flush the coalescing window into a persist-batch + broadcast-frame (§4.2), plus a
   * persist-snapshot when the cadence is due. Emits nothing when no epoch has advanced
   * since the last flush. The batch takes the next durable seq (`lastBatch + 1`) and spans
   * `(fromEpoch, toEpoch)` — content-addressed so replays are byte-identical (E12). The
   * buffered changes are canonicalized (each tag once at its max epoch, lex-sorted) so the
   * batch has exactly ONE serialization across hosts (`canonicalBatch` — behavior-preserving
   * under max-on-write, §4.1).
   *
   * SNAPSHOT CADENCE (§4.2, F12): emit a snapshot when `seq − snapBatch ≥ snapshotEveryBatches`
   * OR `nowMs − lastSnapMs ≥ snapshotIntervalMs`. The seq clause is replay-safe (a pure
   * function of the command journal); the time clause reads the `nowMs` PARAMETER, so a host
   * that replays flushes with the recorded `nowMs` reproduces `snapBatch` exactly.
   *
   * SIZE GATE (§4.2, CACHE-23): before emitting a due snapshot, measure its CANONICAL SHIPPED
   * bytes; if they exceed `maxSnapshotBytes`, SKIP it — emit a `snapshot-skipped-oversize`
   * telemetry effect INSTEAD of `persist-snapshot`, leave `#snapBatch` UNADVANCED (no phantom
   * checkpoint blessed), and let batch/broadcast/pointer proceed normally. `#lastSnapMs` advances
   * so the time clause does not re-fire every flush, but the batch clause stays tripped — a loud
   * re-attempt on each subsequent flush. The gate is journaled (replay-deterministic).
   *
   * Emission order is batch → snapshot → broadcast → advance-pointer, so a host writes every
   * immutable object (`__delta:<seq>`, and `__snap:<seq>` when the cadence fires) BEFORE the
   * ONE terminal `advance-pointer` blesses `__state` (write-then-bless). The pointer is atomic
   * across `lastBatch` AND `snapBatch` — there is no stale intermediate pointer, so no host can
   * issue two sub-second `__state` puts in one flush (D17).
   */
  flush(nowMs: number): readonly FlushEffect[] {
    if (this.#pendingFrom === null) {
      return [];
    }
    const fromEpoch = this.#pendingFrom;
    const toEpoch = this.#epoch;
    const changes = canonicalBatch(this.#pendingChanges);
    this.#pendingFrom = null;
    this.#pendingChanges = [];
    this.#batchSeq += 1;
    const seq = this.#batchSeq;
    const frame: BusFrame = { changes, fromEpoch, toEpoch };
    const effects: FlushEffect[] = [{ frame, kind: "persist-batch", seq }];
    // The time baseline starts at the first flush, so the first batch never trips it.
    this.#lastSnapMs ??= nowMs;
    const dueByBatches = seq - this.#snapBatch >= this.#snapshotEveryBatches;
    const dueByTime = nowMs - this.#lastSnapMs >= this.#snapshotIntervalMs;
    if (dueByBatches || dueByTime) {
      const snapFrame: BusFrame = {
        changes: this.#snapshotChanges(),
        fromEpoch: 0,
        toEpoch,
      };
      // Measure the CANONICAL SHIPPED bytes: the executor persists a snapshot via
      // `JSON.stringify(effect.frame)` (src/registry/execute.ts), so this is the EXACT
      // serialization that would ship — authoritative BECAUSE it is that serialization, not an
      // estimate. UTF-8 byte length is the measure KV enforces on the value. Pure: `TextEncoder`
      // + `JSON.stringify`, no ambient clock/rng.
      const snapBytes = new TextEncoder().encode(
        JSON.stringify(snapFrame)
      ).length;
      if (snapBytes <= this.#maxSnapshotBytes) {
        // Within the ceiling — unchanged: advance both snapshot cursors and emit the checkpoint.
        this.#snapBatch = seq;
        this.#lastSnapMs = nowMs;
        effects.push({ frame: snapFrame, kind: "persist-snapshot", seq });
      } else {
        // OVERSIZE — SKIP: no `persist-snapshot`, `#snapBatch` NOT advanced (no phantom checkpoint
        // blessed). Advance `#lastSnapMs` so the TIME clause does not re-fire every flush; the BATCH
        // clause stays tripped, so each subsequent flush LOUDLY re-attempts. Emit the telemetry
        // effect; the batch, broadcast, and terminal pointer below proceed with the UNCHANGED snapBatch.
        this.#lastSnapMs = nowMs;
        effects.push({
          bytes: snapBytes,
          ceiling: this.#maxSnapshotBytes,
          event: "snapshot-skipped-oversize",
          kind: "telemetry",
        });
      }
    }
    effects.push({ frame, kind: "broadcast-frame" });
    // TERMINAL (§4.2, D17): bless `__state` ONCE, after every immutable-object effect above.
    // `#snapBatch` now reflects this flush (advanced iff the cadence fired), so the single
    // pointer is atomic across both facts — the once-only property is a property of the OUTPUT.
    const pointer: StatePointer = {
      epoch: toEpoch,
      flushedAt: nowMs,
      lastBatch: seq,
      regId: this.#regId,
      snapBatch: this.#snapBatch,
    };
    effects.push({ kind: "advance-pointer", pointer });
    return effects;
  }

  /* References: E5, §4.2. */

  heartbeat(nowMs: number): AdvancePointerEffect {
    return {
      kind: "advance-pointer",
      pointer: {
        epoch: this.#epoch,
        flushedAt: nowMs,
        lastBatch: this.#batchSeq,
        regId: this.#regId,
        snapBatch: this.#snapBatch,
      },
    };
  }

  /**
   * The canonical snapshot change set (§4.2, F12): every tag that has ever advanced, folded
   * over (0, epoch] by the SAME serializer the delta path uses (`canonicalBatch`) — one entry
   * per tag, touched axes only (a 0 axis is absent, never a fabricated 0), per-axis max,
   * lex-sorted. So a snapshot is byte-identical to the single delta batch that would carry the
   * whole watermark map — one serializer, one applier (the unification).
   */
  #snapshotChanges(): TagChange[] {
    const pending: TagChange[] = [];
    for (const [tag, w] of this.#watermarks) {
      pending.push({
        tag,
        ...(w.hardEpoch > 0 ? { hardEpoch: w.hardEpoch } : {}),
        ...(w.softEpoch > 0 ? { softEpoch: w.softEpoch } : {}),
      });
    }
    return canonicalBatch(pending);
  }

  #mutate(
    tags: readonly string[],
    kind: PurgeKind,
    nowMs: number
  ): MutationTransition {
    const fromEpoch = this.#epoch;
    const toEpoch = fromEpoch + 1;
    this.#epoch = toEpoch;
    for (const tag of tags) {
      const w = this.#watermarks.get(tag) ?? { hardEpoch: 0, softEpoch: 0 };
      // Max-on-write: watermarks never regress; soft never regresses hard (§4.1).
      if (kind === "soft") {
        w.softEpoch = Math.max(w.softEpoch, toEpoch);
        this.#pendingChanges.push({ softEpoch: w.softEpoch, tag });
      } else {
        w.hardEpoch = Math.max(w.hardEpoch, toEpoch);
        this.#pendingChanges.push({ hardEpoch: w.hardEpoch, tag });
      }
      this.#watermarks.set(tag, w);
    }
    this.#pendingFrom ??= fromEpoch;
    const effects: readonly ScheduleFlushEffect[] = [
      { deadlineMs: nowMs, kind: "schedule-flush" },
    ];
    return { ack: { epoch: toEpoch }, effects };
  }
}
