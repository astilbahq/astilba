// The pure registry state machine (Coordinator core, thin-shell foundation).
//
// A FUNCTIONAL CORE for the authoritative registry (watermarks, replication and reads),
// commands in, {state transition + effect DESCRIPTIONS} out. It
// performs NO I/O, holds NO callbacks, contains NO promises, and reads NO ambient
// clock. Two hosts embed this one machine — the harness FakeRegistry (Sim bus/store)
// and, later, a Cloudflare Durable Object (storage/alarms/sockets) — so the same
// command script yields the same responses, the same emitted effects, and the same
// content-addressed batch bytes on both.
//
//   - It does not persist   -> it returns a persist-batch effect (the delta batch), and, on
// the snapshot cadence, a persist-snapshot effect (the
//                              canonical batch over (0, epoch]).
//   - It does not broadcast  -> it returns a broadcast-frame effect (the bus frame).
//   - It does not bless      -> it returns ONE terminal advance-pointer effect per flush (the
//                              `__state` pointer), ordered after every immutable-object effect,
// so a host issues exactly one `__state` put per flush.
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

/** The two watermark axes: soft (expire) and hard (delete). */
export type PurgeKind = "soft" | "hard";

/** A tag's dual watermark — independent, monotone, max-on-write. */
export interface WatermarkView {
  readonly softEpoch: number;
  readonly hardEpoch: number;
}

/** Broadcast one replication frame on the invalidation bus. */
export interface BroadcastFrameEffect {
  readonly kind: "broadcast-frame";
  readonly frame: BusFrame;
}

/**
 * Persist one content-addressed delta batch. `seq` is the durable batch number
 * (`lastBatch + 1`); the batch is addressed by the frame's `(fromEpoch, toEpoch)` range, so a
 * replay of the same range is byte-identical. An IMMUTABLE, write-once object; the `__state`
 * pointer that blesses it is the flush's single terminal `advance-pointer` effect.
 */
export interface PersistBatchEffect {
  readonly kind: "persist-batch";
  readonly seq: number;
  readonly frame: BusFrame;
}

/**
 * Persist one content-addressed SNAPSHOT. A snapshot IS the canonical batch
 * over `(0, toEpoch]` — the SAME serializer, canonical bytes, and ≥1-axis rule as a delta
 * batch, folded over EVERY tag that has ever advanced (a never-advanced tag is absent; absence
 * ≡ epoch 0 under max-on-write). Loading it is replaying a single batch from zero. `seq` keys
 * `__snap:<seq>` at the batch seq the checkpoint anchors. An IMMUTABLE, content-addressed
 * object written BEFORE the terminal `advance-pointer` blesses `__state.snapBatch`
 * (write-then-bless).
 */
export interface PersistSnapshotEffect {
  readonly kind: "persist-snapshot";
  readonly seq: number;
  readonly frame: BusFrame;
}

/**
 * Telemetry: a DUE snapshot was SKIPPED because its canonical shipped bytes exceed the configured
 * ceiling. NOT a persisted object — a pure observability effect the flush emits
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

/**
 * The flush's ONE terminal effect: bless the `__state` pointer. The machine emits
 * it EXACTLY ONCE per flush, ordered AFTER every immutable-object effect, so a host issues a
 * single `__state` put per flush — Cloudflare KV throws a 429 on a second write to the same key
 * within 1s, and a redundant intermediate pointer (superseded microseconds later) carries
 * nothing a reader could use. The pointer is atomic across `lastBatch` AND `snapBatch`: it
 * reflects whatever this flush left durable (snapBatch advanced iff the cadence fired).
 */
export interface AdvancePointerEffect {
  readonly kind: "advance-pointer";
  readonly pointer: StatePointer;
}

/**
 * Schedule the next flush no later than `deadlineMs`. The host owns the timer:
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

/** Default snapshot cadence: checkpoint every 500 batches OR every 6h. */
const DEFAULT_SNAPSHOT_EVERY_BATCHES = 500;
const DEFAULT_SNAPSHOT_INTERVAL_MS = 6 * 60 * 60 * 1000;
/** Default snapshot SIZE ceiling: the KV 25 MiB per-value profile. A due
 * snapshot whose canonical shipped bytes exceed this is SKIPPED (never truncated, never a phantom
 * bless) — the machine-owned gate above the executor-side delta bound. */
const DEFAULT_MAX_SNAPSHOT_BYTES = 25 * 1024 * 1024;

/** Machine config knobs. Unset ⇒ the 500-batch / 6h snapshot defaults, the 25 MiB
 * snapshot size ceiling, and the `"default"` registry id. Authored ONLY by the journaled
 * `configure` command (Ruling B). */
export interface RegistryMachineOptions {
  /** Emit a snapshot once `seq − snapBatch` reaches this many batches (replay-safe). */
  readonly snapshotEveryBatches?: number;
  /** Emit a snapshot once `nowMs − lastSnapMs` reaches this many ms (replay via recorded nowMs). */
  readonly snapshotIntervalMs?: number;
  /** Skip a due snapshot whose canonical shipped bytes exceed this ceiling.
   * Journaled like the cadence, so the skip is replay-deterministic. Unset ⇒ the 25 MiB default. */
  readonly maxSnapshotBytes?: number;
  /** The registry id scoping the mirror keyspace. Unset ⇒ `"default"`. Validated
   * `[a-z0-9._-]`, ≤ 64 — a malformed id fails loud here, never mints an ambiguous key. */
  readonly regId?: string;
}

/** The resolved machine config: the snapshot cadence + size ceiling as concrete
 * numbers plus the scoping `regId`. The shell diffs `env` against this to decide whether to
 * journal a fresh `configure` (Ruling B). */
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
 * A GC horizon description. Watermarks whose era predates `collectBeforeMs`
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

/** A per-tag axis accumulator while folding a batch window into canonical form. */
interface TagDelta {
  softEpoch?: number;
  hardEpoch?: number;
}

/**
 * Fold a coalescing window's buffered changes into the CANONICAL batch form:
 *   - every tag appears EXACTLY ONCE, carrying its max soft/hard epoch over the window
 *     (any duplicate tags folded via max-on-write), and
 *   - the changes are ordered LEXICOGRAPHICALLY by tag (UTF-16 code-unit order).
 * Only an axis actually touched in the window is emitted — an untouched axis has no delta
 * over the (fromEpoch, toEpoch] range. Content-addressing makes a batch a pure
 * function of durable state over its range, so it has one serialization; the kernel applies
 * changes via max-on-write, which is commutative and idempotent, so dedup + lex-sort
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
  // across an alarm interval and mints one wider-range batch.
  #pendingFrom: number | null = null;
  #pendingChanges: TagChange[] = [];
  // Snapshot cadence. `#snapBatch` is the batch seq of the last SHIPPED snapshot (last
  // SUCCESS — mirrored into `__state.snapBatch`, advanced only on a persist). `#lastSnapMs` is the
  // `nowMs` baseline for the time clause and means "last ATTEMPT," NOT last success: it advances on
  // every due snapshot INCLUDING an oversize SKIP, so a skip quiets the time clause while
  // the batch clause (`#snapBatch`, unadvanced) stays tripped and loud. Null until the first flush,
  // so the very first batch never trips the time clause.
  #snapBatch = 0;
  #lastSnapMs: number | null = null;
  // The effective snapshot cadence — machine STATE, not a constructor-frozen input (Ruling B).
  // Defaulted here, seeded by the constructor via `configure`, then re-set only by the journaled
  // `configure` command, so a host can never parameterize the machine directly from `env`.
  #snapshotEveryBatches = DEFAULT_SNAPSHOT_EVERY_BATCHES;
  #snapshotIntervalMs = DEFAULT_SNAPSHOT_INTERVAL_MS;
  // The effective snapshot SIZE ceiling — machine STATE, seeded/re-set via
  // `configure` exactly like the cadence, so the oversize skip is replay-deterministic.
  #maxSnapshotBytes = DEFAULT_MAX_SNAPSHOT_BYTES;
  // The mirror-key scoping id — machine STATE, authored ONLY via `configure` (Ruling B),
  // so a replay reproduces the keyspace and `env` can never repoint it outside the journal.
  #regId = DEFAULT_REG_ID;

  constructor(options: RegistryMachineOptions = {}) {
    // The constructor seed is the `configure` command applied once — the SAME code path a host
    // uses (Ruling B), so a direct-machine test and a journaled boot resolve the cadence identically.
    this.configure(options);
  }

  /** The current Coordinator epoch (the correctness clock, machine state). */
  get currentEpoch(): number {
    return this.#epoch;
  }

  /** The batch seq of the last emitted snapshot (`__state.snapBatch`), machine state. */
  get snapBatch(): number {
    return this.#snapBatch;
  }

  /** The last durably-emitted batch seq (`lastBatch`), machine state — both the flush and the heartbeat pointer effects carry it; a host may also read it directly. */
  get lastBatch(): number {
    return this.#batchSeq;
  }

  /** The effective machine config, machine state — the shell diffs `env` against this to decide whether to journal a fresh `configure` (Ruling B). */
  get effectiveConfig(): EffectiveConfig {
    return {
      maxSnapshotBytes: this.#maxSnapshotBytes,
      regId: this.#regId,
      snapshotEveryBatches: this.#snapshotEveryBatches,
      snapshotIntervalMs: this.#snapshotIntervalMs,
    };
  }

  /** The mirror-key scoping id, machine state — the host reads it to build the
   * scoped keyspace it writes the flush effects to (`mirrorKeys(regId)`). */
  get regId(): string {
    return this.#regId;
  }

  /**
   * Apply an effective snapshot cadence as machine STATE. A JOURNALED command (Ruling B):
   * `env` may AUTHOR this — it never parameterizes the machine directly — so a replay reproduces
   * the cadence AT ITS JOURNAL POSITION, prospective by construction. Unset knobs fall to the
   * defaults, exactly as the constructor seed does. Never retroactive: it changes only how future
   * flushes trip the cadence, so `snapBatch` stays monotone across a config change.
   */
  configure(options: RegistryMachineOptions): void {
    this.#snapshotEveryBatches =
      options.snapshotEveryBatches ?? DEFAULT_SNAPSHOT_EVERY_BATCHES;
    this.#snapshotIntervalMs =
      options.snapshotIntervalMs ?? DEFAULT_SNAPSHOT_INTERVAL_MS;
    this.#maxSnapshotBytes =
      options.maxSnapshotBytes ?? DEFAULT_MAX_SNAPSHOT_BYTES;
    // `regId` is validated at configure time — the charset gate that makes the scoped
    // keyspace injective (no `:` inside an id). Unset falls to `"default"`, exactly as the
    // constructor seed does, so a cadence-only reconfigure that omits it re-seeds the default.
    const regId = options.regId ?? DEFAULT_REG_ID;
    assertRegId(regId);
    this.#regId = regId;
  }

  /** The monotone high-water GC horizon in ms — never retracts. */
  get gcHorizonMs(): number {
    return this.#gcHorizonMs;
  }

  /** Authoritative dual watermark for a tag; absent tags read as a zero watermark. */
  watermarkOf(tag: string): WatermarkView {
    const w = this.#watermarks.get(tag);
    return { hardEpoch: w?.hardEpoch ?? 0, softEpoch: w?.softEpoch ?? 0 };
  }

  /**
   * Live watermark knowledge for these tags. A primary read is always authoritative
   * and known; an untouched tag reads as a known zero watermark, never as unknown.
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

  /** Soft purge: max-on-write bump of `softEpoch` for each tag. */
  expire(tags: readonly string[], nowMs: number): MutationTransition {
    return this.#mutate(tags, "soft", nowMs);
  }

  /** Hard purge: max-on-write bump of `hardEpoch` for each tag. */
  delete(tags: readonly string[], nowMs: number): MutationTransition {
    return this.#mutate(tags, "hard", nowMs);
  }

  /** Register a retention lease; the GC horizon rises to the high-water. */
  registerRetention(retentionMs: number): void {
    this.#gcHorizonMs = Math.max(this.#gcHorizonMs, retentionMs);
  }

  /**
   * Describe the GC horizon at `nowMs`. Pure: the machine collects nothing;
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
   * Flush the coalescing window into a persist-batch + broadcast-frame, plus a
   * persist-snapshot when the cadence is due. Emits nothing when no epoch has advanced
   * since the last flush. The batch takes the next durable seq (`lastBatch + 1`) and spans
   * `(fromEpoch, toEpoch)` — content-addressed so replays are byte-identical. The
   * buffered changes are canonicalized (each tag once at its max epoch, lex-sorted) so the
   * batch has exactly ONE serialization across hosts (`canonicalBatch` — behavior-preserving
   * under max-on-write).
   *
   * SNAPSHOT CADENCE: emit a snapshot when `seq − snapBatch ≥ snapshotEveryBatches`
   * OR `nowMs − lastSnapMs ≥ snapshotIntervalMs`. The seq clause is replay-safe (a pure
   * function of the command journal); the time clause reads the `nowMs` PARAMETER, so a host
   * that replays flushes with the recorded `nowMs` reproduces `snapBatch` exactly.
   *
   * SIZE GATE: before emitting a due snapshot, measure its CANONICAL SHIPPED
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
   * issue two sub-second `__state` puts in one flush.
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
    // TERMINAL: bless `__state` ONCE, after every immutable-object effect above.
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

  /**
   * A liveness heartbeat. Refreshes the `__state` pointer's freshness field
   * (`flushedAt`) at `nowMs` while advancing NO cursor: it reads the CURRENT epoch/lastBatch/
   * snapBatch and reuses the terminal `advance-pointer` effect, so a host blesses `__state`
   * through the SAME executor a flush uses (one hot-key put, never a raw write). Emitted
   * as a SINGLE pointer effect: the freshness fields move, the monotone cursors do not.
   *
   * STATE-NEUTRAL by construction — it mutates nothing, so the monotone cursors cannot move and
   * replaying it is a pure no-op. That is the property that lets a host decline to journal a
   * heartbeat for replay-exactness (replay is exact over the NON-heartbeat commands alone). The
   * shell decides WHEN to issue it (an env-gated cadence, Ruling B); the machine only supplies the
   * pointer bytes — `env` authors the command, it never parameterizes the machine.
   */
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
   * The canonical snapshot change set: every tag that has ever advanced, folded
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
      // Max-on-write: watermarks never regress; soft never regresses hard.
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
