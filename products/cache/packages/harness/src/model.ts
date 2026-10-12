import type { Consistency } from "@astilba/cache";
import fc from "fast-check";

import { mkKey, mkTag } from "./tags";
import { world } from "./world";
import type { World, WorldConfig } from "./world";

// The async model-based layer for fast-check (§ Task 3). The kernel API is
// Promise-based, so commands are `AsyncCommand`s driven by `fc.asyncModelRun`
// inside `fc.asyncProperty`; race-shaped invariants add `fc.scheduler()` to
// reorder promise resolutions under SimClock. The oracle tracks, per key, the set
// of legally-servable values given the visibility bounds of §1; the core assertion
// everywhere is: observed serve ⊆ legal serves.

/** A tiny fixed key/tag space keeps the oracle tractable: key `k<i>` carries tag `t<i>`. */
export const KEY_SPACE = 3;

/**
 * The WIDE layout's OWN space constants (CACHE-28-ii Stage 1) — deliberately distinct
 * from the default generator's `KEY_SPACE`, so touching these can never perturb the
 * regression-floor generator. `TAG_SPACE_WIDE < KEY_SPACE_WIDE` makes ONE tag fan out
 * to multiple keys (`k0`,`k1`,`k2` all carry `t0`), the configuration the default 1:1
 * generator can never produce — the substrate for the co-tag hard-purge property.
 */
export const KEY_SPACE_WIDE = 3;
export const TAG_SPACE_WIDE = 1;

/**
 * The key/tag index widths a generator lays its commands over (CACHE-28-ii). The default
 * generator uses `NARROW_LAYOUT` (`tagSpace === keySpace === KEY_SPACE`, so key `k<i>` ↔
 * tag `t<i>` 1:1, exactly as before); the wide generator uses `tagSpace < keySpace` so a
 * tag covers several keys. A command carries its layout so `keyOf`/`tagOf` resolve the
 * same strings its generator intended — the ONLY thing that varies between lanes.
 */
export interface SpaceLayout {
  readonly keySpace: number;
  readonly tagSpace: number;
}

/** The default (regression-floor) 1:1 layout — key `k<i>` carries its own tag `t<i>`. A
 * command constructed without an explicit layout uses this, so `commandsArb` is byte-identical. */
export const NARROW_LAYOUT: SpaceLayout = {
  keySpace: KEY_SPACE,
  tagSpace: KEY_SPACE,
};

/** The wide layout — `TAG_SPACE_WIDE` tags over `KEY_SPACE_WIDE` keys, so one tag fans out. */
export const WIDE_LAYOUT: SpaceLayout = {
  keySpace: KEY_SPACE_WIDE,
  tagSpace: TAG_SPACE_WIDE,
};

export interface Model {
  /**
   * key → the set of values that may legally be served right now (§1 bounds). This is
   * the SOLE representation of hard-purge visibility (invariant 1): a hard delete clears
   * the key's legal set to empty, so every value born before the (globally visible in this
   * SimClock-serialized model) delete becomes illegal to serve; only a subsequent fill
   * re-legitimizes the key, and only with the value THAT fill offered to the origin.
   */
  readonly legal: Map<string, Set<string>>;
  /**
   * tag → the keys observed carrying it (CACHE-28-ii, §4.1). Populated by every `getOrSet`
   * that offers a value for `key` under `tag`; read by a HARD `delete` to fan the tag's
   * purge to EVERY co-tagged key's legal set — the shared-tag facet of invariant 1 (a hard
   * watermark is per-TAG, so it invalidates all entries carrying the tag, not just one key).
   * For the default 1:1 generator each tag indexes exactly its single key, so the fan-out
   * clears the same one key the pre-Stage-1 model did — behaviourally identical (backward
   * compatible). A SOFT `expire` never touches this: a soft-purged value stays grace-servable
   * and therefore LEGAL, so widening it would make the oracle wrongly reject a legal stale serve.
   */
  readonly tagKeys: Map<string, Set<string>>;
}

export interface Real {
  readonly world: World;
}

export function freshModel(): Model {
  return { legal: new Map(), tagKeys: new Map() };
}

export function modelSetup(
  config: WorldConfig = {}
): () => { model: Model; real: Real } {
  return () => ({ model: freshModel(), real: { world: world(config) } });
}

type Command = fc.AsyncCommand<Model, Real>;

const keyOf = (i: number, layout: SpaceLayout = NARROW_LAYOUT): string =>
  mkKey(`k${i % layout.keySpace}`);
const tagOf = (i: number, layout: SpaceLayout = NARROW_LAYOUT): string =>
  `t${i % layout.tagSpace}`;

function legalSet(model: Model, key: string): Set<string> {
  const existing = model.legal.get(key);
  if (existing) {
    return existing;
  }
  const created = new Set<string>();
  model.legal.set(key, created);
  return created;
}

/** Record that `key` carries `tag` (CACHE-28-ii) — the tag→keys index a hard delete fans over. */
function indexTagKey(model: Model, tag: string, key: string): void {
  const keys = model.tagKeys.get(tag);
  if (keys) {
    keys.add(key);
    return;
  }
  model.tagKeys.set(tag, new Set([key]));
}

/**
 * Co-tag fan-out reach counter (CACHE-28-ii Stage 1, R3). A wide property run exercises
 * fan-out iff a hard `delete` clears at least one key OTHER than the one it named — the
 * work only a shared-tag layout can produce, which the 1:1 default generator structurally
 * never does. `coTagFanCaseCount` aggregates DISTINCT such runs (keyed on model identity —
 * each `fc.asyncModelRun` gets a fresh model, so counting once per model counts once per
 * case). A property asserts this ≥ a conservative floor in `afterAll`: if the generator
 * ever drifts to starve shared-tag layouts, the floor reds — generator drift becomes a
 * failing test rather than a silent slide back to a 1:1 no-op. Inert for the default
 * generator (1:1 ⇒ a tag indexes only its own key ⇒ no sibling ⇒ never counted).
 */
const countedFanModels = new WeakSet<Model>();
let coTagFanCaseCount = 0;

/** Cases (distinct property runs) that exercised co-tag fan-out since the last reset. */
export function coTagFanCases(): number {
  return coTagFanCaseCount;
}

/** Zero the fan-out case counter — a property calls this in `beforeAll` so its `afterAll`
 * floor measures only its own runs, never another file's accumulation. */
export function resetCoTagFanCases(): void {
  coTagFanCaseCount = 0;
}

// ── CACHE-28-ii Stage 2: per-dimension DELIVERY-reach accounting ─────────────────────────────
//
// Each chaos dimension the alphabet adds ships a reached-counter so the chaos property can FLOOR
// it: a dimension whose perturbation never actually FIRED in a run was not exercised, and its
// floor reds (the anti-inert-knob teeth in property form — a generated-but-idle knob is a
// no-op in generator costume). A case counts once per DISTINCT property run (deduped on model
// identity — each `fc.asyncModelRun` gets a fresh model AND a fresh world, so the real-side
// counters start at 0 and `> 0` means the perturbation fired THIS run).
//
// A dimension is REACHED only when the REAL side genuinely performed the perturbation — a frame
// was dropped, a buffered pair was swapped, a per-key delay held a newer pointer stale, a strong
// read landed on a hard-dead key — read from bus/store delivery counters and the model's own
// purge bookkeeping. NEVER from the kernel's suspect / applied / head state: a reach counter that
// re-derived the kernel's convergence logic would certify the kernel against itself (circular).
// These are DELIVERY facts (what was accepted/dropped/reordered), the same layer the oracle's
// `legal` set lives on.

const gapCounted = new WeakSet<Model>();
const reorderCounted = new WeakSet<Model>();
const delayedStateCounted = new WeakSet<Model>();
const heartbeatCounted = new WeakSet<Model>();
const strongCounted = new WeakSet<Model>();
/** Models in whose run a `HeartbeatCommand` actually executed (a pointer freshness refresh). */
const heartbeatModels = new WeakSet<Model>();
/** model → keys currently HARD-DEAD: cleared by an accepted hard delete and not yet refilled.
 * The substrate for the strong-read reach — a strong read of a key in this set exercises the
 * `observed ⊆ legal` serve-dead arm on a value the registry purged (CACHE-26's shape). Tracked
 * on the MODEL's accepted-purge bookkeeping (a delete cleared it, a fill re-legitimized it),
 * never on kernel state. */
const deadKeysByModel = new WeakMap<Model, Set<string>>();

let gapReachedCount = 0;
let reorderReachedCount = 0;
let delayedStateReachedCount = 0;
let heartbeatReachedCount = 0;
let strongReachedCount = 0;

/** Cases that actually dropped a bus frame (a `gap` fired) since the last reset. */
export function gapReachedCases(): number {
  return gapReachedCount;
}
/** Cases that actually delivered two frames out of order (a swap fired) since the last reset. */
export function reorderReachedCases(): number {
  return reorderReachedCount;
}
/** Cases whose reader observed a `__state` pointer a per-key delay held stale, since the reset. */
export function delayedStateReachedCases(): number {
  return delayedStateReachedCount;
}
/** Cases where a heartbeat freshened the pointer within a gap-hidden-purge + stalled-pointer
 * episode (the CACHE-27 context), since the last reset. */
export function heartbeatReachedCases(): number {
  return heartbeatReachedCount;
}
/** Cases where a STRONG read landed on a hard-dead key (the E16 live-check serve-dead arm),
 * since the last reset. */
export function strongReachedCases(): number {
  return strongReachedCount;
}

/** Zero every Stage-2 dimension reach counter — a property calls this in `beforeAll` so its
 * `afterAll` floors measure only its own runs, never another file's accumulation. */
export function resetChaosReaches(): void {
  gapReachedCount = 0;
  reorderReachedCount = 0;
  delayedStateReachedCount = 0;
  heartbeatReachedCount = 0;
  strongReachedCount = 0;
}

/** The model's hard-dead key set (keys cleared by an accepted delete, awaiting a refill). */
function deadKeysOf(model: Model): Set<string> {
  const existing = deadKeysByModel.get(model);
  if (existing) {
    return existing;
  }
  const created = new Set<string>();
  deadKeysByModel.set(model, created);
  return created;
}

/** Mark `key` hard-dead for the strong-read reach IFF it currently carries a legal value (was
 * filled and alive) — a delete on a never-filled key produces no serve-dead probe, so it is not
 * marked. Call BEFORE clearing the legal set. */
function markHardDead(model: Model, dead: Set<string>, key: string): void {
  const legal = model.legal.get(key);
  if (legal !== undefined && legal.size > 0) {
    dead.add(key);
  }
}

/**
 * Fold the run's DELIVERY facts into the per-dimension reach counters (once per case). Called at
 * the tail of every command's `run` so a perturbation that fired mid-sequence is caught by any
 * later command. Reads only real-side delivery counters + the model's purge bookkeeping; the
 * kernel's own suspect/applied/head state is never consulted (that would be circular).
 */
function noteChaosReaches(model: Model, real: Real): void {
  const { bus, store } = real.world;
  if (bus.gapFireCount > 0 && !gapCounted.has(model)) {
    gapCounted.add(model);
    gapReachedCount += 1;
  }
  if (bus.reorderFireCount > 0 && !reorderCounted.has(model)) {
    reorderCounted.add(model);
    reorderReachedCount += 1;
  }
  if (store.keyDelayHiddenReads > 0 && !delayedStateCounted.has(model)) {
    delayedStateCounted.add(model);
    delayedStateReachedCount += 1;
  }
  // Heartbeat reach: a heartbeat ran AND a purge was dropped (gap) AND the reader observed a
  // delay-held-stale pointer — the exact gap + delayed-`__state` + heartbeat episode the
  // CACHE-27 serve-dead lives in. All three are delivery facts, no kernel state.
  if (
    heartbeatModels.has(model) &&
    bus.gapFireCount > 0 &&
    store.keyDelayHiddenReads > 0 &&
    !heartbeatCounted.has(model)
  ) {
    heartbeatCounted.add(model);
    heartbeatReachedCount += 1;
  }
}

/** getOrSet: fills from origin and asserts the served value is legally servable. */
export class GetOrSetCommand implements Command {
  constructor(
    readonly iso: number,
    readonly keyIndex: number,
    readonly value: string,
    readonly layout: SpaceLayout = NARROW_LAYOUT,
    /** Read consistency (§1). `strong` (CACHE-28-ii Stage 2) forces the E16 live check, which
     * enriches knowledge and so makes the serve-dead arm property-reachable for free — a strong
     * read of a hard-dead value would be `∉ legal` (CACHE-26's shape). It enters under the SAME
     * `observed ⊆ legal` assertion; there is NO second strong-legal set (that would be a second
     * kernel — declined). Default `eventual` keeps every prior generator byte-identical. */
    readonly consistency: Consistency = "eventual"
  ) {}

  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    const key = keyOf(this.keyIndex, this.layout);
    const tag = tagOf(this.keyIndex, this.layout);
    // Record the key↔tag association BEFORE the fill (CACHE-28-ii): a later hard delete on
    // `tag` fans its purge over every key indexed here. 1:1 layout ⇒ one key per tag ⇒ the
    // pre-Stage-1 single-key clear, unchanged.
    indexTagKey(model, tag, key);
    // Strong-read reach (Stage 2): a strong read of a key the model knows is hard-dead exercises
    // the serve-dead arm on a registry-purged value. Counted BEFORE the fill re-legitimizes it.
    if (
      this.consistency === "strong" &&
      deadKeysOf(model).has(key) &&
      !strongCounted.has(model)
    ) {
      strongCounted.add(model);
      strongReachedCount += 1;
    }
    const iso = real.world.isolate(this.iso % real.world.isolateCount);
    const observed = await iso.cache.getOrSet({
      // exactOptionalPropertyTypes: only pass `consistency` when it is non-default, so an
      // eventual command's request object is byte-identical to the pre-Stage-2 shape.
      ...(this.consistency === "eventual"
        ? {}
        : { consistency: this.consistency }),
      factory: async () => this.value,
      key,
      tags: [mkTag(tag)],
    });
    // Origin truth: this fill OFFERS `this.value` to the origin, so after it, `this.value`
    // is legally servable for `key` — a miss/refill returns it, a hit returns a prior
    // still-legal value (marking the offered value legal on a hit merely widens the set to
    // a value the app asked to cache; it can never mask a served-dead bug, since a
    // hard-deleted value differs from a live refill's value). We commit the value we KNOW
    // we offered — NEVER the observed serve — which is what makes the assertion below a
    // genuine `observed ⊆ legal` oracle rather than the former `add(observed); has(observed)`
    // tautology. A visible hard delete cleared this key's legal set (DeleteCommand); only a
    // fill like this one re-legitimizes a value, and only the offered value.
    const legal = legalSet(model, key);
    legal.add(this.value);
    // A fill re-legitimizes the key: it is no longer hard-dead (a subsequent strong read of it
    // is an ordinary live read, not a serve-dead probe).
    deadKeysOf(model).delete(key);
    // Core assertion: observed serve ⊆ legal serves. Fires when the kernel serves a value
    // never legally born for `key` — e.g. a value from before a visible hard purge whose
    // legal set was cleared and which was NOT re-offered as this fill's `this.value`.
    if (!legal.has(observed)) {
      throw new Error(`illegal serve for ${key}: ${observed}`);
    }
    noteChaosReaches(model, real);
  }

  toString(): string {
    const strong = this.consistency === "eventual" ? "" : ", strong";
    return `getOrSet(iso=${this.iso}, ${keyOf(this.keyIndex, this.layout)}, "${this.value}"${strong})`;
  }
}

/** expire (soft): value stays grace/SWR-servable — no legal-set change. */
export class ExpireCommand implements Command {
  constructor(
    readonly iso: number,
    readonly keyIndex: number,
    readonly layout: SpaceLayout = NARROW_LAYOUT
  ) {}

  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    const iso = real.world.isolate(this.iso % real.world.isolateCount);
    await iso.cache.expire({ tag: mkTag(tagOf(this.keyIndex, this.layout)) });
    // A purge PUBLISHES a frame, which is where a dropped/reordered frame fires — fold those
    // delivery facts into the reach counters.
    noteChaosReaches(model, real);
  }

  toString(): string {
    return `expire(iso=${this.iso}, ${tagOf(this.keyIndex, this.layout)})`;
  }
}

/** delete (hard): once visible, EVERY co-tagged key's pre-delete values are no longer legal. */
export class DeleteCommand implements Command {
  constructor(
    readonly iso: number,
    readonly keyIndex: number,
    readonly layout: SpaceLayout = NARROW_LAYOUT
  ) {}

  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    const key = keyOf(this.keyIndex, this.layout);
    const tag = tagOf(this.keyIndex, this.layout);
    const iso = real.world.isolate(this.iso % real.world.isolateCount);
    await iso.cache.delete({ tag: mkTag(tag) });
    // A visible hard delete makes every pre-delete value illegal to serve: clear the legal
    // set to empty (§1/§4.1, invariant 1). The watermark is per-TAG, so the purge fans over
    // EVERY key carrying `tag` — not just the one keyOf(keyIndex) names. Clear this key
    // (preserving the pre-Stage-1 behavior exactly, incl. a delete on a never-filled key) AND
    // every co-tagged sibling the tag→keys index recorded. 1:1 layout ⇒ the index holds only
    // this one key ⇒ identical to before; wide layout ⇒ all co-tagged keys go dead together.
    const dead = deadKeysOf(model);
    markHardDead(model, dead, key);
    model.legal.set(key, new Set());
    const coTagged = model.tagKeys.get(tag);
    for (const coKey of coTagged ?? []) {
      markHardDead(model, dead, coKey);
      model.legal.set(coKey, new Set());
    }
    // Fan-out reach accounting (R3): count this run once if the delete cleared a co-tagged
    // key distinct from the one it named — the shared-tag work a 1:1 layout can't produce.
    if (
      coTagged !== undefined &&
      !countedFanModels.has(model) &&
      [...coTagged].some((coKey) => coKey !== key)
    ) {
      countedFanModels.add(model);
      coTagFanCaseCount += 1;
    }
    // A hard delete PUBLISHES a frame too (the drop/reorder firing point) — fold delivery facts.
    noteChaosReaches(model, real);
  }

  toString(): string {
    return `delete(iso=${this.iso}, ${tagOf(this.keyIndex, this.layout)})`;
  }
}

export class AdvanceCommand implements Command {
  constructor(readonly ms: number) {}

  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    await real.world.advance(this.ms);
    // Advancing delivers buffered/reordered frames and lets a suspect read resync off the
    // (possibly stalled) pointer — the point where reorder + delayed-state facts settle.
    noteChaosReaches(model, real);
  }

  toString(): string {
    return `advance(${this.ms}ms)`;
  }
}

export class DropFrameCommand implements Command {
  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    real.world.chaos.dropNextFrames(1);
    noteChaosReaches(model, real);
  }

  toString(): string {
    return "dropFrame()";
  }
}

/** Buffer subsequent bus frames for `ms` (CACHE-28-ii Stage 2, dimension c). The precondition for
 * an observable reorder — two frames must be in flight at once for a swap to have anything to swap.
 * Drives the LOGICAL `world.chaos.delayFrames` knob, never `world.bus` directly. */
export class DelayFramesCommand implements Command {
  constructor(readonly ms: number) {}

  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    real.world.chaos.delayFrames(this.ms);
    noteChaosReaches(model, real);
  }

  toString(): string {
    return `delayFrames(${this.ms}ms)`;
  }
}

/** Swap the next two BUFFERED frames so a higher (non-contiguous) frame can arrive ahead of a
 * lower one (CACHE-28-ii Stage 2, dimension c). Out-of-order delivery — a no-op unless a prior
 * `delayFrames` has two frames buffered when the second publishes. */
export class ReorderFramesCommand implements Command {
  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    real.world.chaos.reorderFrames();
    noteChaosReaches(model, real);
  }

  toString(): string {
    return "reorderFrames()";
  }
}

/** Stall the `__state` pointer's visibility by `ms` (CACHE-28-ii Stage 2, dimension a). The reader
 * resyncs off a lagging pointer — a stale pointer whose epoch trails a durable purge. Addresses the
 * LOGICAL mirror-key name `"__state"`; the harness resolves it to the physical scoped key. */
export class DelayStateCommand implements Command {
  constructor(readonly ms: number) {}

  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    real.world.chaos.delayKey("__state", this.ms);
    noteChaosReaches(model, real);
  }

  toString(): string {
    return `delayState(${this.ms}ms)`;
  }
}

/** Fire an E5 heartbeat (CACHE-28-ii Stage 2, dimension d): the coordinator refreshes the
 * `__state` pointer's `flushedAt` freshness field WITHOUT minting any epoch. A DELIVERY
 * perturbation only — freshness motion must never be read as catch-up (CACHE-27), so it changes
 * neither `legal` nor `tagKeys`. Its serve-dead relevance is that a heartbeat-freshened stale
 * pointer, read while a gap hides a purge, is exactly where a convergence anchor tied to freshness
 * would wrongly clear suspicion. */
export class HeartbeatCommand implements Command {
  check(): boolean {
    return true;
  }

  async run(model: Model, real: Real): Promise<void> {
    heartbeatModels.add(model);
    await real.world.registry.heartbeat();
    noteChaosReaches(model, real);
  }

  toString(): string {
    return "heartbeat()";
  }
}

export class DelayReplicaCommand implements Command {
  constructor(
    readonly index: number,
    readonly ms: number
  ) {}

  check(): boolean {
    return true;
  }

  async run(_model: Model, real: Real): Promise<void> {
    real.world.chaos.delayReplica(
      this.index % real.world.isolateCount,
      this.ms
    );
  }

  toString(): string {
    return `delayReplica(${this.index}, ${this.ms}ms)`;
  }
}

export class ReconnectCommand implements Command {
  check(): boolean {
    return true;
  }

  async run(_model: Model, real: Real): Promise<void> {
    real.world.chaos.reconnect();
  }

  toString(): string {
    return "reconnect()";
  }
}

/** The default command arbitrary spanning fills, purges, time, and chaos. */
export function commandsArb(maxCommands = 20): fc.Arbitrary<Iterable<Command>> {
  const iso = fc.nat(3);
  const key = fc.nat(KEY_SPACE - 1);
  const value = fc.string({ maxLength: 8 });
  const ms = fc.integer({ max: 5000, min: 1 });
  return fc.commands(
    [
      fc
        .tuple(iso, key, value)
        .map(([i, k, v]) => new GetOrSetCommand(i, k, v)),
      fc.tuple(iso, key).map(([i, k]) => new ExpireCommand(i, k)),
      fc.tuple(iso, key).map(([i, k]) => new DeleteCommand(i, k)),
      ms.map((m) => new AdvanceCommand(m)),
      fc.constant(new DropFrameCommand()),
      fc.tuple(iso, ms).map(([i, m]) => new DelayReplicaCommand(i, m)),
      fc.constant(new ReconnectCommand()),
    ],
    { maxCommands }
  );
}

/**
 * The WIDE + CHAOS command arbitrary (CACHE-28-ii Stage 2): the WIDE layout's fills/purges (so
 * hard deletes still fan over co-tagged siblings) laid under the full delivery-perturbation
 * alphabet — dropped frames (b), buffered + reordered frames (c), a stalled `__state` pointer (a),
 * an E5 heartbeat (d) — plus strong reads as a first-class action (e). Every chaos command is a
 * DELIVERY perturbation on the REAL side ONLY: none touches `legal`/`tagKeys`, so the oracle's
 * `observed ⊆ legal` legality bound is invariant under gap/delay/reorder/heartbeat — a
 * registry-accepted hard purge stays illegal to serve under ANY delivery schedule. The generator
 * mixes `delayFrames` (the buffering a reorder needs) with `reorderFrames`, and `delayState` with
 * `heartbeat`, so the reach floors (below) find the gap-hidden-purge + stalled-pointer episodes.
 */
export function commandsArbChaos(
  maxCommands = 32
): fc.Arbitrary<Iterable<Command>> {
  const iso = fc.nat(3);
  const key = fc.nat(KEY_SPACE_WIDE - 1);
  const value = fc.string({ maxLength: 8 });
  const ms = fc.integer({ max: 5000, min: 1 });
  // A stalled pointer must OUTLAST the run's advances to still read stale at a LATER suspect read
  // (a small stall elapses before the read that would observe it). The run's total advance is
  // bounded well under this range, so a `delayState` reliably keeps the pointer stale for the run.
  const stallMs = fc.integer({ max: 200_000, min: 40_000 });
  // Bias reads toward strong (2/3) so the E16 live-check serve-dead arm and the stalled-pointer
  // observation points (both driven by reads) are exercised often enough to floor.
  const consistency: fc.Arbitrary<Consistency> = fc.constantFrom(
    "eventual",
    "strong",
    "strong"
  );
  const getOrSet = fc
    .tuple(iso, key, value, consistency)
    .map(([i, k, v, c]) => new GetOrSetCommand(i, k, v, WIDE_LAYOUT, c));
  return fc.commands(
    [
      // Reads appear twice: a read is the observation point for BOTH the stalled-pointer arm
      // (a suspect read resyncs off the stale `__state`) and the strong-read arm.
      getOrSet,
      getOrSet,
      fc.tuple(iso, key).map(([i, k]) => new ExpireCommand(i, k, WIDE_LAYOUT)),
      fc.tuple(iso, key).map(([i, k]) => new DeleteCommand(i, k, WIDE_LAYOUT)),
      ms.map((m) => new AdvanceCommand(m)),
      fc.constant(new DropFrameCommand()),
      ms.map((m) => new DelayFramesCommand(m)),
      fc.constant(new ReorderFramesCommand()),
      stallMs.map((m) => new DelayStateCommand(m)),
      fc.constant(new HeartbeatCommand()),
    ],
    { maxCommands }
  );
}

/**
 * The convergence-EPISODE arbitrary (CACHE-28-ii Stage 2): a structured skeleton with RANDOM
 * parameters (which key/isolate, timings, stall size, read consistency) that reliably walks the
 * CACHE-27 serve-dead shape every run — the rare episode a uniform generator almost never
 * assembles (which is why the deterministic sacred traces construct it by hand). The skeleton:
 *
 *   warm kW  →  stall `__state`  →  drop the HARD purge's frame (gap)  →  heartbeat (freshen the
 *   stalled pointer)  →  strong read of the now-dead kW.
 *
 * (It does NOT reorder — out-of-order delivery is a distinct dimension, floored by the reorder
 * episode below. This episode's serve-dead comes from the dropped hard frame + stalled pointer.)
 *
 * Under the correct kernel the gap-opened episode declares a live-channel head the stalled pointer
 * and the freshness bump never satisfy, so every read fails closed and REFILLS — the oracle
 * (`observed ⊆ legal`) holds. It is the alphabet's teeth against a convergence anchor that cleared
 * on pointer freshness / a bare resync (CACHE-27): such a kernel serves the gap-hidden dead `warm`
 * and reds here. Every step is a DELIVERY perturbation — `legal`/`tagKeys` are untouched — so the
 * episode reliably FIRES the delayed-`__state`, heartbeat, gap and strong-read reaches that floor
 * those dimensions (reorder is floored by the separate reorder episode). Randomizing the parameters
 * keeps it a property, not a single trace.
 */
export function commandsArbConvergenceEpisode(): fc.Arbitrary<
  Iterable<Command>
> {
  const WRITER = 0;
  const READER = 1;
  return fc
    .record({
      a1: fc.integer({ max: 2000, min: 1 }),
      a2: fc.integer({ max: 2000, min: 1 }),
      a3: fc.integer({ max: 2000, min: 1 }),
      key: fc.nat(KEY_SPACE_WIDE - 1),
      // The stall must outlast the episode's advances so the pointer is still stale at the reads.
      stall: fc.integer({ max: 200_000, min: 40_000 }),
      w: fc.string({ maxLength: 6 }),
    })
    .map((p): Command[] => {
      // Two co-tagged keys under the shared WIDE tag: the single hard delete kills BOTH, so one
      // read can be the eventual convergence-anchor witness and the other the strong live-check
      // witness — neither refill re-legitimizes the other's key.
      const kA = p.key;
      const kB = (p.key + 1) % KEY_SPACE_WIDE;
      return [
        // Fill + STRONG-PRIME both keys. The strong read's live check records EVERY tag the entry
        // carries — the shared tag AND the reserved per-key/per-namespace tags — into the reader's
        // local map. That completeness is the precondition for a later missed hard purge to be
        // SERVED rather than refilled: a value classifies FRESH only when ALL its tags are known and
        // none is dead, so a warm-but-incomplete map (a soft frame teaches only the shared tag)
        // would fail closed regardless of any convergence bug.
        new GetOrSetCommand(READER, kA, `warmA-${p.w}`, WIDE_LAYOUT),
        new GetOrSetCommand(READER, kA, `primeA-${p.w}`, WIDE_LAYOUT, "strong"),
        new GetOrSetCommand(READER, kB, `warmB-${p.w}`, WIDE_LAYOUT),
        new GetOrSetCommand(READER, kB, `primeB-${p.w}`, WIDE_LAYOUT, "strong"),
        // Stall the pointer the reader will resync off after the gap.
        new DelayStateCommand(p.stall),
        new AdvanceCommand(p.a1),
        // Hide the HARD purge behind a dropped frame (gap ⇒ suspect); both keys are now
        // registry-dead, and the stalled pointer never carries the purge over the live channel.
        new DropFrameCommand(),
        new DeleteCommand(WRITER, kA, WIDE_LAYOUT),
        new AdvanceCommand(p.a2),
        // Freshen the stalled pointer's `flushedAt` — the CACHE-27 bait (no epoch minted).
        new HeartbeatCommand(),
        new AdvanceCommand(p.a3),
        // Eventual read of kA — the convergence-anchor witness. Suspect, no REACHED head ⇒ fail
        // closed ⇒ refill. It resyncs off the stalled pointer (delayed-`__state` reach). A kernel
        // that cleared suspicion on a declared-but-unreached head (or on freshness) serves the
        // dead `warmA` here (CACHE-27).
        new GetOrSetCommand(READER, kA, `refillA-${p.w}`, WIDE_LAYOUT),
        // Strong read of the co-dead kB — floors the strong reach and participates in
        // value-membership (a dead serve is `∉ legal`); under the correct kernel it refills. It does
        // NOT witness CACHE-26 (the strong-MISS born-dead serve, owned by strong-fill-livecheck); its
        // value here is exercising a strong action inside the convergence composition.
        new GetOrSetCommand(
          READER,
          kB,
          `refillB-${p.w}`,
          WIDE_LAYOUT,
          "strong"
        ),
      ];
    });
}

/**
 * The REORDER episode (CACHE-28-ii Stage 2, dimension c) — `serve-dead-out-of-order`'s episode
 * TWIN: a warm reader, then a buffered pair swapped so a HIGHER-epoch HARD delete arrives
 * NON-CONTIGUOUSLY ahead of a lower soft expire — the out-of-order delivery the default generators
 * never assemble. A fixed post-buffer advance (well past the frame delay) guarantees delivery, so
 * the swap reliably fires (`reorderFrames` is a no-op without two buffered frames) and floors the
 * reorder dimension. The soft frame is kept for the reach; the hard delete makes the oracle
 * NON-trivial. Its serve-dead is CLASSIFY-shaped, not convergence-shaped: reorder perturbs DELIVERY,
 * never durable truth, so the reordered hard purge is always LEARNABLE — resync replays it from the
 * durable mirror regardless of frame order — and the known-path `classify` is the defence that must
 * catch it (blinding `classify`'s hard-purge verdict reds this episode; blinding the convergence
 * anchor does NOT, because resync applies the delivered purge anyway). Randomized keys/values/timing
 * keep it a property.
 */
export function commandsArbReorderEpisode(): fc.Arbitrary<Iterable<Command>> {
  const WRITER = 0;
  const READER = 1;
  const DELIVER_MS = 3000; // > every `frameDelay` below, so the buffered pair always delivers.
  return fc
    .record({
      frameDelay: fc.integer({ max: 500, min: 1 }),
      key: fc.nat(KEY_SPACE_WIDE - 1),
      tail: fc.integer({ max: 2000, min: 1 }),
      v: fc.string({ maxLength: 6 }),
    })
    .map((p): Command[] => [
      // Warm the reader so a reordered HARD purge has a live value to kill.
      new GetOrSetCommand(READER, p.key, `warm-${p.v}`, WIDE_LAYOUT),
      new AdvanceCommand(1),
      // Buffer two frames and swap them: a soft expire (kept for the reorder reach) and a HARD
      // delete, so the hard purge arrives NON-CONTIGUOUSLY ahead of the soft one. The hard purge
      // must not be lost — under C″ the reader stays suspect until it REACHES the declared head,
      // so it fails closed and refills; a convergence anchor clearing on the declared-but-unreached
      // head would serve the now-dead `warm` (value-illegal). That is this episode's teeth.
      new DelayFramesCommand(p.frameDelay),
      new ReorderFramesCommand(),
      new ExpireCommand(WRITER, p.key, WIDE_LAYOUT),
      new DeleteCommand(WRITER, p.key, WIDE_LAYOUT),
      new AdvanceCommand(DELIVER_MS),
      new GetOrSetCommand(READER, p.key, `r-${p.v}`, WIDE_LAYOUT),
      new AdvanceCommand(p.tail),
      new GetOrSetCommand(READER, p.key, `r2-${p.v}`, WIDE_LAYOUT),
    ]);
}

/**
 * The WIDE command arbitrary (CACHE-28-ii Stage 1): the SAME command classes as
 * `commandsArb`, laid over `WIDE_LAYOUT` (`TAG_SPACE_WIDE` tags over `KEY_SPACE_WIDE`
 * keys) so ONE tag fans out to several keys. `key` ranges over the wider key space; each
 * command derives its tag as `keyIndex % TAG_SPACE_WIDE`, so a hard `delete` on a shared
 * tag must invalidate EVERY co-tagged key — the facet the 1:1 default generator can never
 * reach. The oracle (observed ⊆ legal) is unchanged; only the layout differs, so a
 * co-tagged sibling served its pre-delete value trips the same `illegal serve` assertion.
 */
export function commandsArbWide(
  maxCommands = 20
): fc.Arbitrary<Iterable<Command>> {
  const iso = fc.nat(3);
  const key = fc.nat(KEY_SPACE_WIDE - 1);
  const value = fc.string({ maxLength: 8 });
  const ms = fc.integer({ max: 5000, min: 1 });
  return fc.commands(
    [
      fc
        .tuple(iso, key, value)
        .map(([i, k, v]) => new GetOrSetCommand(i, k, v, WIDE_LAYOUT)),
      fc.tuple(iso, key).map(([i, k]) => new ExpireCommand(i, k, WIDE_LAYOUT)),
      fc.tuple(iso, key).map(([i, k]) => new DeleteCommand(i, k, WIDE_LAYOUT)),
      ms.map((m) => new AdvanceCommand(m)),
      fc.constant(new DropFrameCommand()),
      fc.tuple(iso, ms).map(([i, m]) => new DelayReplicaCommand(i, m)),
      fc.constant(new ReconnectCommand()),
    ],
    { maxCommands }
  );
}
