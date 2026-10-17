import type {
  Bus,
  BusEvent,
  BusFrame,
  CasOrder,
  CasRecord,
  Codec,
  Lock,
  LockHandle,
  Registry,
  RegistryAck,
  Store,
  StoreValue,
  StoreWriteOptions,
  Subscription,
  TagKnowledge,
  Validity,
} from "@astilba/cache";
import type { StatePointer } from "@astilba/cache/registry";

import type {
  ResyncPolicy,
  ResyncStatus,
  SnapshotPolicy,
} from "../../../../src/invalidation";
import type { BusHarness } from "../../runner/bus";
import type { ContractReader, ReaderHarness } from "../../runner/reader";
import type { IsolationHarness, RegistryHarness } from "../../runner/registry";
import type { StoreHarness } from "../../runner/store";
import { adaptInvalidation, buildReaderParts } from "../reader-parts";
import type { ReaderParts } from "../reader-parts";

// Deliberately-WRONG driver fixtures. Each pairs with a meta-assertion (teeth.test.ts)
// that the TARGETED contract check FAILS against it — proving the check discriminates
// a liar, not merely that it passes the reference (the __ns-guard / vacuity lesson,
// These impls are otherwise
// minimal; only the one targeted property is broken per fixture.

/** A store that drops every read — loses read-your-own-write. Targets: RCW check. */
class RcwLosingStore implements Store {
  async get(_key: string): Promise<StoreValue | undefined> {
    // BUG: drops every read, so a written value is never observed again.
    return undefined;
  }

  async set(
    _key: string,
    _value: string,
    _options?: StoreWriteOptions
  ): Promise<void> {
    // Accepted, but never observable on a subsequent read.
  }

  async delete(_key: string): Promise<void> {
    // No stored state to remove.
  }
}

export function makeRcwLosingStoreHarness(): () => StoreHarness {
  return (): StoreHarness => ({ store: new RcwLosingStore() });
}

/**
 * A store that throttles by throwing a BARE Error — no `code`/`retryable`, so the
 * failure carries no structural StoreWriteError classification. Targets: throttle check.
 * Correct on every other axis; only the classification is missing (the kernel could not
 * tell a throttle from any other write failure).
 */
class UnclassifiedThrottleStore implements Store {
  readonly #data = new Map<string, string>();
  #throttleNext = false;

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(
    key: string,
    value: string,
    _options?: StoreWriteOptions
  ): Promise<void> {
    if (this.#throttleNext) {
      this.#throttleNext = false;
      // BUG: throttles with an UNCLASSIFIED error — no `code`, so `isStoreWriteError`
      // returns false and a `throttled` outcome is indistinguishable from a generic fault.
      throw new Error(`write throttled: ${key}`);
    }
    this.#data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }

  armThrottle(): void {
    this.#throttleNext = true;
  }
}

export function makeUnclassifiedThrottleStoreHarness(): () => StoreHarness {
  return (): StoreHarness => {
    const store = new UnclassifiedThrottleStore();
    return {
      forceThrottle: (): void => {
        store.armThrottle();
      },
      store,
    };
  };
}

/**
 * A store that, on an oversize value, SILENTLY truncates to the limit and RESOLVES —
 * never signalling `too_large`, dropping bytes without telling the caller. Targets: the
 * oversize check. It knows its own limit so it can demonstrate the truncation.
 */
class TruncatingOversizeStore implements Store {
  readonly #data = new Map<string, string>();
  readonly #maxValueBytes: number;

  constructor(maxValueBytes: number) {
    this.#maxValueBytes = maxValueBytes;
  }

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(
    key: string,
    value: string,
    _options?: StoreWriteOptions
  ): Promise<void> {
    // BUG: silently truncates an oversize value and RESOLVES — no `too_large` signal, and
    // the stored value is a shortened lie (a subsequent read returns fewer bytes).
    const stored =
      new TextEncoder().encode(value).length > this.#maxValueBytes
        ? value.slice(0, this.#maxValueBytes)
        : value;
    this.#data.set(key, stored);
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }
}

export function makeTruncatingOversizeStoreHarness(
  maxValueBytes: number
): () => StoreHarness {
  return (): StoreHarness => ({
    store: new TruncatingOversizeStore(maxValueBytes),
  });
}

/** A CAS that ignores the expected token — non-atomic. Targets: CAS no-regress check. */
class NonAtomicCasStore implements Store {
  readonly #data = new Map<string, string>();
  readonly #cas = new Map<string, CasRecord>();
  #token = 0;

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(
    key: string,
    value: string,
    _options?: StoreWriteOptions
  ): Promise<void> {
    this.#data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }

  async seed(key: string, order: CasOrder): Promise<void> {
    this.#token += 1;
    this.#cas.set(key, { order, token: this.#token });
  }

  async load(key: string): Promise<CasRecord | undefined> {
    return this.#cas.get(key);
  }

  async swap(
    key: string,
    _expectedToken: number,
    next: CasOrder
  ): Promise<number | undefined> {
    // BUG: applies unconditionally, never checking `expectedToken`. A losing racer's
    // late, lower-fenced write clobbers the committed higher one → (epoch, fence) regresses.
    this.#token += 1;
    this.#cas.set(key, { order: next, token: this.#token });
    return this.#token;
  }
}

export function makeNonAtomicCasHarness(): () => StoreHarness {
  return (): StoreHarness => {
    const store = new NonAtomicCasStore();
    return { cas: store, store };
  };
}

/** A registry whose soft purge zeroes the hard watermark. Targets: dual-independence. */
class SoftRegressesHardRegistry implements Registry {
  readonly regId = "default";
  #epoch = 0;
  readonly #soft = new Map<string, number>();
  readonly #hard = new Map<string, number>();

  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    const through = this.#epoch;
    return tags.map(
      (tag): TagKnowledge => ({
        hardEpoch: this.#hard.get(tag) ?? 0,
        known: true,
        softEpoch: this.#soft.get(tag) ?? 0,
        throughEpoch: through,
      })
    );
  }

  async expire(tags: readonly string[]): Promise<RegistryAck> {
    this.#epoch += 1;
    for (const tag of tags) {
      this.#soft.set(tag, this.#epoch);
      // BUG: a soft purge regresses the hard watermark to zero.
      this.#hard.set(tag, 0);
    }
    return { epoch: this.#epoch };
  }

  async delete(tags: readonly string[]): Promise<RegistryAck> {
    this.#epoch += 1;
    for (const tag of tags) {
      this.#hard.set(tag, this.#epoch);
    }
    return { epoch: this.#epoch };
  }

  async registerRetention(_retentionMs: number): Promise<void> {
    // No horizon state; registration is a no-op for this fixture.
  }
}

export function makeSoftRegressesHardRegistryHarness(): () => RegistryHarness {
  return (): RegistryHarness => ({ registry: new SoftRegressesHardRegistry() });
}

/** A registry whose soft batch omits BOTH axes. Targets: axis-empty change. */
class AxisEmptyBatchRegistry implements Registry {
  readonly regId = "default";
  #epoch = 0;
  readonly #frames: BusFrame[] = [];

  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    const through = this.#epoch;
    return tags.map(
      (): TagKnowledge => ({
        hardEpoch: 0,
        known: true,
        softEpoch: 0,
        throughEpoch: through,
      })
    );
  }

  async expire(tags: readonly string[]): Promise<RegistryAck> {
    const fromEpoch = this.#epoch;
    this.#epoch += 1;
    // BUG: emits a change carrying NEITHER `softEpoch` nor `hardEpoch` — an axis-empty
    // change (illegal; it asserts nothing, so a reader learns nothing from it).
    this.#frames.push({
      changes: tags.map((tag) => ({ tag })),
      fromEpoch,
      toEpoch: this.#epoch,
    });
    return { epoch: this.#epoch };
  }

  async delete(tags: readonly string[]): Promise<RegistryAck> {
    const fromEpoch = this.#epoch;
    this.#epoch += 1;
    // Correct: the hard batch carries its axis (only the soft path above is broken).
    this.#frames.push({
      changes: tags.map((tag) => ({ hardEpoch: this.#epoch, tag })),
      fromEpoch,
      toEpoch: this.#epoch,
    });
    return { epoch: this.#epoch };
  }

  async registerRetention(_retentionMs: number): Promise<void> {
    // No horizon state; registration is a no-op for this fixture.
  }

  frames(): readonly BusFrame[] {
    return this.#frames;
  }
}

export function makeAxisEmptyBatchRegistryHarness(): () => RegistryHarness {
  return (): RegistryHarness => {
    const registry = new AxisEmptyBatchRegistry();
    return {
      emittedBatches: async (): Promise<readonly BusFrame[]> =>
        registry.frames(),
      registry,
    };
  };
}

/**
 * A mirror writer that DROPS the registry prefix — every registry blesses the SAME unscoped
 * `__state`/`__delta:1` regardless of regId, so a second registry's write clobbers the first and a
 * reader cannot tell whose pointer it read (exactly the collision scopes away). Targets: the
 * isolation check. The payload still echoes `regId`, so it is the KEY collision — not the echo —
 * that the check catches: the second `writeUnder` overwrites the one shared `__state`, so neither
 * registry's SCOPED key is ever populated.
 */
class PrefixDroppingMirrorStore implements Store {
  readonly #data = new Map<string, string>();

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(
    key: string,
    value: string,
    _options?: StoreWriteOptions
  ): Promise<void> {
    this.#data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }
}

export function makePrefixDroppingMirrorHarness(): () => IsolationHarness {
  return (): IsolationHarness => {
    const store = new PrefixDroppingMirrorStore();
    return {
      store,
      writeUnder: async (regId: string): Promise<void> => {
        // BUG: unscoped keys — the `__reg:<regId>:` prefix (and thus per-registry isolation) is
        // dropped, so every registry writes the SAME two keys.
        await store.set(
          "__state",
          JSON.stringify({
            epoch: 1,
            flushedAt: 1,
            lastBatch: 1,
            regId,
            snapBatch: 0,
          })
        );
        await store.set(
          "__delta:1",
          JSON.stringify({ changes: [], fromEpoch: 0, toEpoch: 1 })
        );
      },
    };
  };
}

/** A codec whose encode is nondeterministic — not byte-stable. Targets: round-trip. */
export function makeNonByteStableCodec(): () => Codec {
  return (): Codec => {
    let calls = 0;
    return {
      decode: (value: string): unknown => JSON.parse(value) as unknown,
      encode: (value: unknown): string => {
        calls += 1;
        // BUG: appends call-count whitespace, so encode(v) differs across calls.
        return JSON.stringify(value) + " ".repeat(calls);
      },
      id: "broken/1",
    };
  };
}

/** A lock whose fence never advances — non-monotone. Targets: fence monotonicity. */
class NonMonotoneLock implements Lock {
  async acquire(_key: string): Promise<LockHandle> {
    return {
      // BUG: a constant fence — a later lease never out-fences an earlier one.
      fence: 1,
      release: async (): Promise<void> => {
        // No lease state to release.
      },
    };
  }
}

export function makeNonMonotoneLockHarness(): () => Lock {
  return (): Lock => new NonMonotoneLock();
}

/**
 * A bus that re-opens the transport WITHOUT re-emitting `reset`. Targets: B1. It is
 * correct everywhere else — it emits `reset` on the initial subscribe and delivers
 * frames faithfully — so the ONLY reason the check rejects it is the missing
 * reconnect handshake: a subscriber that missed a frame while disconnected keeps
 * stale warm trust, exactly the hole the reset handshake closes.
 */
/**
 * Shared plumbing for the deliberately-broken bus fixtures: the handler set, the connection
 * flag, faithful frame delivery, and the DEFAULT establishment (a bare `reset`, no `hello`).
 * Each subclass varies ONLY the axis it breaks — the reconnect handshake (ForgetfulBus /
 * SilentBus) or the `hello` head declaration (UnderdeclaringBus). Sharing the common body keeps
 * every fixture honest: it differs from the reference on its ONE targeted axis, not by
 * incidental divergence.
 */
abstract class BrokenBusBase implements Bus {
  protected readonly handlers = new Set<(event: BusEvent) => void>();
  protected connected = true;

  /** Default establishment: a bare `reset`, no `hello`. Overridden only where a fixture
   * must also emit a hello. */
  subscribe(onEvent: (event: BusEvent) => void): Subscription {
    this.handlers.add(onEvent);
    if (this.connected) {
      onEvent({ kind: "reset" });
    }
    return {
      close: (): void => {
        this.handlers.delete(onEvent);
      },
    };
  }

  /** Faithful frame delivery (correct on every fixture). */
  publish(frame: BusFrame): void {
    if (this.connected) {
      this.emit({ frame, kind: "frame" });
    }
  }

  disconnect(): void {
    this.connected = false;
  }

  abstract reconnect(): void;

  protected emit(event: BusEvent): void {
    for (const handler of this.handlers) {
      handler(event);
    }
  }
}

class ForgetfulBus extends BrokenBusBase {
  reconnect(): void {
    // BUG: re-establishes the transport but omits the `reset` handshake.
    this.connected = true;
  }
}

export function makeForgetfulBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const bus = new ForgetfulBus();
    return {
      bus,
      publish: (frame: BusFrame): void => {
        bus.publish(frame);
      },
      reconnect: (): void => {
        bus.reconnect();
      },
    };
  };
}

/**
 * A bus that establishes with a bare `reset` and NEVER a `hello`. Correct on every
 * other axis — reset on establishment, faithful frames, re-emitted reset on reconnect — so the
 * ONLY reason the handshake rule rejects it is the missing head declaration: a caught-up subscriber gets no
 * live-channel head to anchor convergence on and can never re-trust its warm map (the
 * heartbeat is no longer an anchor). Targets: the handshake rule (reset → hello → frames).
 */
class SilentBus extends BrokenBusBase {
  reconnect(): void {
    this.connected = true;
    // BUG: re-establishes with a bare `reset` — still no `hello`. The default subscribe
    // (inherited) is already reset-only, so this fixture NEVER emits a hello on any establishment.
    this.emit({ kind: "reset" });
  }
}

export function makeSilentBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const bus = new SilentBus();
    return {
      bus,
      publish: (frame: BusFrame): void => {
        bus.publish(frame);
      },
      reconnect: (): void => {
        bus.reconnect();
      },
    };
  };
}

/**
 * A bus whose `hello` head UNDER-declares — it sits one below the channel's true head, so a
 * caught-up subscriber believes it has NOT reached the head and stays suspect forever.
 * Correct on the structural axis — reset → hello → frames, faithful delivery — so the ONLY
 * violation is the lower bound (`head` ≥ every previously-emitted `toEpoch`). Targets: the
 * lower-bound obligation. It tracks the true head over ALL broadcasts (delivery ≠ truth) so the
 * under-declaration is demonstrable against the frames the check observed.
 */
class UnderdeclaringBus extends BrokenBusBase {
  #head = 0;

  override subscribe(onEvent: (event: BusEvent) => void): Subscription {
    // reset first (inherited), THEN the mis-declared hello — structurally correct order.
    const sub = super.subscribe(onEvent);
    if (this.connected) {
      onEvent({ head: this.#underdeclaredHead(), kind: "hello" });
    }
    return sub;
  }

  override publish(frame: BusFrame): void {
    // Tracks the true head (delivery ≠ truth), so the under-declaration below is measurable.
    this.#head = Math.max(this.#head, frame.toEpoch);
    super.publish(frame);
  }

  reconnect(): void {
    this.connected = true;
    this.emit({ kind: "reset" });
    // BUG: the hello head sits below the channel's true head — the lower bound the handshake rule requires.
    this.emit({ head: this.#underdeclaredHead(), kind: "hello" });
  }

  /** One below the true head — a lower bound violated whenever a frame has been emitted. */
  #underdeclaredHead(): number {
    return Math.max(0, this.#head - 1);
  }
}

export function makeUnderdeclaringBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const bus = new UnderdeclaringBus();
    return {
      bus,
      publish: (frame: BusFrame): void => {
        bus.publish(frame);
      },
      reconnect: (): void => {
        bus.reconnect();
      },
    };
  };
}

/**
 * A bus whose `gap` head UNDER-declares — on a dropped frame it signals a `gap` but with a
 * head BELOW a frame it already delivered, so a reader that reaches the highest delivered frame
 * believes it accounted for the hole. Correct on every other axis (reset → hello →
 * frames, faithful delivery, a gap ON loss); the ONLY violation is the gap's lower bound. Targets:
 * the lower-bound obligation.
 */
class UnderdeclaringGapBus extends BrokenBusBase {
  #head = 0;
  #dropNext = 0;

  override subscribe(onEvent: (event: BusEvent) => void): Subscription {
    // Correct establishment: reset (inherited) THEN a truthful hello (intact).
    const sub = super.subscribe(onEvent);
    if (this.connected) {
      onEvent({ head: this.#head, kind: "hello" });
    }
    return sub;
  }

  override publish(frame: BusFrame): void {
    this.#head = Math.max(this.#head, frame.toEpoch);
    if (!this.connected) {
      return;
    }
    if (this.#dropNext > 0) {
      this.#dropNext -= 1;
      // BUG: the gap head sits below a frame already delivered — the gap rule requires `head` ≥ every
      // frame emitted before the signal, so a caught-up reader would converge past the hole.
      this.emit({ head: this.#underdeclaredGapHead(), kind: "gap" });
      return;
    }
    this.emit({ frame, kind: "frame" });
  }

  reconnect(): void {
    this.connected = true;
    this.emit({ kind: "reset" });
    this.emit({ head: this.#head, kind: "hello" });
  }

  dropNext(n: number): void {
    this.#dropNext += n;
  }

  /** Two below the true head — below a frame already delivered once ≥2 frames have been emitted. */
  #underdeclaredGapHead(): number {
    return Math.max(0, this.#head - 2);
  }
}

export function makeUnderdeclaringGapBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const bus = new UnderdeclaringGapBus();
    return {
      bus,
      dropNext: (n: number): void => {
        bus.dropNext(n);
      },
      publish: (frame: BusFrame): void => {
        bus.publish(frame);
      },
      reconnect: (): void => {
        bus.reconnect();
      },
    };
  };
}

// ── Deliberately-WRONG READERS (reader contract) ────────────────────────────────────
// Each decorates the REAL Invalidation (via the shared parts) and breaks EXACTLY ONE property,
// so the reader teeth (reader-teeth.test.ts) prove the matching check discriminates it. The
// reference passes every check; each liar fails only the one it targets.

/** Compose a reader from a base with one or more methods overridden (the single BUG per fixture). */
function decorateReader(
  base: ContractReader,
  overrides: Partial<ContractReader>
): ContractReader {
  return { ...base, ...overrides };
}

function assembleLiar(
  regId: string,
  override: (
    base: ContractReader,
    parts: ReaderParts
  ) => Partial<ContractReader>
): ReaderHarness {
  const parts = buildReaderParts(regId);
  const base = adaptInvalidation(parts.inval, parts.bus);
  return {
    keys: parts.keys,
    reader: decorateReader(base, override(base, parts)),
    regId: parts.regId,
    store: parts.store,
  };
}

const LIAR_REG_ID = "reader-x";
const TERMINAL_GAP: ResyncStatus = "terminal-gap";

/** Does the seeded pointer JSON declare a snapBatch trailing lastBatch (the legal, first-class
 * lagging case the StrictSnap liar wrongly rejects)? Split across statements to keep each
 * expression simple. */
function pointerLagsSnap(value: string): boolean {
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    return false;
  }
  if (typeof parsed !== "object" || parsed === null) {
    return false;
  }
  if (!("snapBatch" in parsed) || !("lastBatch" in parsed)) {
    return false;
  }
  const { snapBatch, lastBatch } = parsed;
  return (
    typeof snapBatch === "number" &&
    typeof lastBatch === "number" &&
    snapBatch < lastBatch
  );
}

/**
 * A reader that SERVES a value THROUGH suspicion instead of failing closed — the exact
 * violation the stale-pointer and unfilled-hole checks forbid. Targets: stale-pointer + hole.
 */
export function makeServeThroughReaderHarness(): () => ReaderHarness {
  return (): ReaderHarness =>
    assembleLiar(LIAR_REG_ID, (base) => ({
      verdict: async (
        tags: readonly string[],
        bornEpoch: number
      ): Promise<Validity> => {
        const v = await base.verdict(tags, bornEpoch);
        // BUG: while suspect, serves the warm value through instead of returning unknown.
        return v === "unknown" && base.suspect() ? "fresh" : v;
      },
    }));
}

/**
 * A reader that PROBES PAST THE POINTER — fetching a delta beyond `lastBatch` (unblessed) during
 * resync. The read-logging store catches it. Targets: probe-past-pointer.
 */
export function makeOverFetchReaderHarness(): () => ReaderHarness {
  return (): ReaderHarness =>
    assembleLiar(LIAR_REG_ID, (base, parts) => ({
      resync: async (policy?: ResyncPolicy): Promise<ResyncStatus> => {
        const status = await base.resync(policy);
        // BUG: probes a delta PAST the pointer (an unblessed seq) — an over-fetch.
        await parts.store.get(parts.keys.deltaKey(9999));
        return status;
      },
    }));
}

/**
 * A reader whose terminal detection is BLINDED — it never rules `terminal-gap`, signalling retry
 * forever (it would spin, never fail-closed-permanent). Targets: terminal-hole.
 */
export function makeNoTerminalReaderHarness(): () => ReaderHarness {
  return (): ReaderHarness =>
    assembleLiar(LIAR_REG_ID, (base) => ({
      resyncViaSnapshot: async (
        policy?: SnapshotPolicy
      ): Promise<ResyncStatus> => {
        const status = await base.resyncViaSnapshot(policy);
        // BUG: converts a terminal gap into an endless retry — never fail-closed-permanent.
        return status === TERMINAL_GAP
          ? { kind: "transient-miss", seq: 2 }
          : status;
      },
    }));
}

/**
 * A reader that treats a LAGGING snapBatch (legal, first-class normal) as an anomalous gap,
 * refusing to converge over the GC'd floor. Targets: lagging-snapBatch.
 */
export function makeStrictSnapReaderHarness(): () => ReaderHarness {
  return (): ReaderHarness =>
    assembleLiar(LIAR_REG_ID, (base, parts) => ({
      resyncViaSnapshot: async (
        policy?: SnapshotPolicy
      ): Promise<ResyncStatus> => {
        const raw = await parts.store.get(parts.keys.stateKey);
        if (raw && pointerLagsSnap(raw.value)) {
          // BUG: a snapBatch trailing lastBatch is legal — treating it as terminal is the anomaly.
          return TERMINAL_GAP;
        }
        return await base.resyncViaSnapshot(policy);
      },
    }));
}

/**
 * A reader that treats a BLESSED-BUT-MISSING snap as fully covered — 'claims coverage it lacks',
 * converging over a broken chain instead of ruling terminal. Targets: blessed-but-missing-snap.
 */
export function makeUnderCoveringSnapReaderHarness(): () => ReaderHarness {
  return (): ReaderHarness =>
    assembleLiar(LIAR_REG_ID, (base) => ({
      resyncViaSnapshot: async (
        policy?: SnapshotPolicy
      ): Promise<ResyncStatus> => {
        const status = await base.resyncViaSnapshot(policy);
        // BUG: a terminal (unfillable) gap is silently declared converged — coverage it lacks.
        return status === TERMINAL_GAP ? "converged" : status;
      },
    }));
}

/** A reader whose derivative RAISE is blinded — a missed-heartbeat writer is never noticed.
 * Targets: derivative raising. */
export function makeNeverRaiseReaderHarness(): () => ReaderHarness {
  return (): ReaderHarness =>
    assembleLiar(LIAR_REG_ID, () => ({
      notePointerObservation: (
        _pointer: StatePointer,
        _nowMs: number
      ): void => {
        // BUG: never raises derivative suspicion on consecutive missed heartbeats.
      },
    }));
}

/** A reader whose poll predicate is STUCK false — it never polls, whatever the elapsed time.
 * Targets: pollDue. */
export function makeStuckPollReaderHarness(): () => ReaderHarness {
  return (): ReaderHarness =>
    assembleLiar(LIAR_REG_ID, () => ({
      pollDue: (
        _nowMs: number,
        _lastPollMs: number,
        _thresholdMs: number
      ): boolean => false,
    }));
}
