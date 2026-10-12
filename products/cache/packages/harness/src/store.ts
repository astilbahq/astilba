import type {
  Store,
  StoreMetadata,
  StoreValue,
  StoreWriteError,
  StoreWriteOptions,
} from "@astilba/cache";

import type { SimClock } from "./clock";

// FakeStore — an eventually-consistent KV store modelled as N replica views with
// per-(replica,key) visibility delay, per-key write-throttle emulation (1 write/s
// → 429), TTL enforcement via SimClock, and injectable get/set failures (§8, §4.2).
// A single-view, zero-delay, un-throttled store is the strongly-consistent Redis /
// L1 stand-in.

/**
 * Thrown by `set` when a key is written faster than 1/s (KV's hard 429) — §8, N10.
 * SATISFIES the structural `StoreWriteError` contract (`throttled`, retryable) WITHOUT
 * sharing a class with the kernel: the kernel discriminates it via `isStoreWriteError`
 * (structural), never `instanceof` (E13). `implements` proves the shape conforms.
 */
export class WriteThrottleError extends Error implements StoreWriteError {
  readonly key: string;
  readonly code = "throttled";
  readonly retryable = true;

  constructor(key: string) {
    super(`write throttled: ${key} (1 write/s/key)`);
    this.name = "WriteThrottleError";
    this.key = key;
  }
}

/**
 * Thrown by `set` when a value exceeds the store's `maxValueBytes` (§8: KV's 25 MiB
 * value ceiling). Like `WriteThrottleError` it SATISFIES `StoreWriteError` structurally
 * (`too_large`, non-retryable) — the value is REJECTED, never truncated. Internal:
 * consumers discriminate via `isStoreWriteError`, never by this class's identity, so it
 * needs no export.
 */
class OversizeValueError extends Error implements StoreWriteError {
  readonly key: string;
  readonly code = "too_large";
  readonly retryable = false;

  constructor(key: string, sizeBytes: number, maxBytes: number) {
    super(`value too large: ${key} (${sizeBytes} > ${maxBytes} bytes)`);
    this.name = "OversizeValueError";
    this.key = key;
  }
}

/** An injected store failure (chaos) — §9 (KV down, l2_read_error / l2_write_error). */
export class InjectedStoreError extends Error {
  constructor(op: "get" | "set" | "delete", key: string) {
    super(`injected store ${op} failure: ${key}`);
    this.name = "InjectedStoreError";
  }
}

interface Version {
  readonly seq: number;
  readonly writtenAtMs: number;
  readonly value?: string;
  readonly metadata?: StoreMetadata;
  readonly expireAtMs?: number;
  readonly tombstone: boolean;
}

export interface FakeStoreOptions {
  /** Number of replica views (1 = strongly consistent). Default 1. */
  readonly replicas?: number;
  /** Per-key write throttle window in ms (0 = no throttle). Default 0. */
  readonly throttleWindowMs?: number;
  /**
   * Max value size in bytes; a `set` of a larger value rejects `too_large` (§8: KV's
   * 25 MiB ceiling) WITHOUT storing or truncating. Default: undefined ⇒ unlimited (the
   * v0.1 behavior, unchanged).
   */
  readonly maxValueBytes?: number;
  /**
   * What a `set` COSTS in logical ms — the clock advances by this much before the version lands,
   * so a caller that awaits the write observes time having passed (§8: a durable write is not
   * free). The one knob that models write LATENCY; `delayReplica`/`delayKey` model replication
   * lag (a completed write held invisible) and move no clock. Default 0 ⇒ free writes, the
   * pre-existing timeline exactly.
   */
  readonly writeCostMs?: number;
}

/**
 * The shared durable store. Reads are served through a replica view (`replica(i)`)
 * that respects that replica's visibility delay; writes land on the authoritative
 * timeline. Holds data keys as well as the `__state` / `__delta:*` replication keys.
 */
export class FakeStore {
  readonly #clock: SimClock;
  readonly #replicas: number;
  readonly #throttleWindowMs: number;
  readonly #maxValueBytes: number | undefined;
  #writeCostMs: number;
  readonly #versions = new Map<string, Version[]>();
  #seq = 0;
  readonly #lastWriteMs = new Map<string, number>();

  // Chaos: visibility delay ms by replica index, and per-key overrides.
  #replicaDelayMs: number[] = [];
  readonly #keyDelayMs = new Map<string, number>();
  /** Reads that a per-KEY visibility delay (the `delayKey` chaos knob) actively held stale —
   * a newer written version existed and would have been visible but for the delay (CACHE-28-ii
   * Stage 2). The reach signal for the delayed-`__state` dimension: the pointer the reader
   * resyncs off lags a durable write. Only KEY delays count (a replica-wide delay is a
   * different dimension), and only when a newer version was genuinely hidden — an armed delay
   * over a key with nothing newer to hide never increments (anti-inert-knob). */
  #keyDelayHiddenReads = 0;
  readonly #forceThrottleKeys = new Set<string>();
  readonly #injectGet = new Set<string>();
  readonly #injectSet = new Set<string>();

  constructor(clock: SimClock, options: FakeStoreOptions = {}) {
    this.#clock = clock;
    this.#replicas = options.replicas ?? 1;
    this.#throttleWindowMs = options.throttleWindowMs ?? 0;
    this.#maxValueBytes = options.maxValueBytes;
    this.#writeCostMs = options.writeCostMs ?? 0;
    this.#replicaDelayMs = Array.from(
      { length: this.#replicas },
      (): number => 0
    );
  }

  get replicaCount(): number {
    return this.#replicas;
  }

  /** Reads a per-key `delayKey` delay held stale so far (a newer version was hidden). Fresh per
   * world, so `> 0` means the delayed-`__state` arm genuinely fired this run (CACHE-28-ii). */
  get keyDelayHiddenReads(): number {
    return this.#keyDelayHiddenReads;
  }

  #nextSeq(): number {
    this.#seq += 1;
    return this.#seq;
  }

  /** A `Store` bound to replica `index`: reads use that replica's visibility. */
  replica(index: number): Store {
    if (index < 0 || index >= this.#replicas) {
      throw new Error(
        `FakeStore.replica: index ${index} out of range [0,${this.#replicas})`
      );
    }
    // Methods are async so injected failures/throttles surface as rejected
    // promises (as a real KV driver would), never synchronous throws.
    return {
      delete: async (key) => {
        await Promise.resolve().then(() => {
          this.#tombstone(key);
        });
      },
      get: async (key) =>
        await Promise.resolve().then(() => this.#read(index, key)),
      set: async (key, value, options) => {
        await Promise.resolve().then(() => {
          this.#write(key, value, options);
        });
      },
    };
  }

  #visibilityDelay(replica: number, key: string): number {
    return this.#keyDelayMs.get(key) ?? this.#replicaDelayMs[replica] ?? 0;
  }

  #read(replica: number, key: string): StoreValue | undefined {
    if (this.#injectGet.has(key)) {
      throw new InjectedStoreError("get", key);
    }
    const now = this.#clock.now();
    const history = this.#versions.get(key);
    if (!history) {
      return undefined;
    }
    const delay = this.#visibilityDelay(replica, key);
    let visible: Version | undefined;
    for (const version of history) {
      if (version.writtenAtMs + delay <= now) {
        visible = version;
      }
    }
    // Reach accounting for the delayed-`__state` dimension (CACHE-28-ii Stage 2): a per-KEY
    // delay HID a newer version this read — one written (`writtenAtMs <= now`) but not yet
    // visible under the key delay. Counted only for key delays (not replica-wide) and only
    // when something newer was genuinely masked, so an armed-but-idle delay never inflates it.
    const keyDelay = this.#keyDelayMs.get(key);
    if (
      keyDelay !== undefined &&
      keyDelay > 0 &&
      history.some(
        (v) => v.writtenAtMs <= now && v.writtenAtMs + keyDelay > now
      )
    ) {
      this.#keyDelayHiddenReads++;
    }
    if (!visible || visible.tombstone) {
      return undefined;
    }
    if (visible.expireAtMs !== undefined && now >= visible.expireAtMs) {
      return undefined;
    }
    return visible.metadata === undefined
      ? { value: visible.value ?? "" }
      : { metadata: visible.metadata, value: visible.value ?? "" };
  }

  #write(key: string, value: string, options?: StoreWriteOptions): void {
    if (this.#injectSet.has(key)) {
      throw new InjectedStoreError("set", key);
    }
    // A write COSTS time (§8: a real durable write is not instantaneous). Charged before the
    // version lands, so the value's `writtenAtMs` is when the write COMPLETED — which is what a
    // caller that awaited it observes. Distinct from `delayReplica`/`delayKey`, which hold a
    // completed write INVISIBLE for a while: those model replication lag, this models latency,
    // and only this one moves the clock. Default 0 ⇒ writes are free and the timeline is exactly
    // as it was before this knob existed.
    if (this.#writeCostMs > 0) {
      this.#clock.advance(this.#writeCostMs);
    }
    if (this.#maxValueBytes !== undefined) {
      // `TextEncoder`, not `Buffer`: the harness is loaded by the workerd integration
      // lane (which runs without `nodejs_compat`, so `Buffer` is not a global) as well
      // as by Node and Bun. UTF-8 byte length is identical either way.
      const sizeBytes = new TextEncoder().encode(value).length;
      if (sizeBytes > this.#maxValueBytes) {
        // Reject oversize BEFORE appending: the value never enters #versions and is
        // never truncated (§8: KV 25 MiB ceiling; E13 classified `too_large`).
        throw new OversizeValueError(key, sizeBytes, this.#maxValueBytes);
      }
    }
    const now = this.#clock.now();
    const last = this.#lastWriteMs.get(key);
    const throttled =
      this.#forceThrottleKeys.has(key) ||
      (this.#throttleWindowMs > 0 &&
        last !== undefined &&
        now - last < this.#throttleWindowMs);
    if (throttled) {
      throw new WriteThrottleError(key);
    }
    this.#lastWriteMs.set(key, now);
    const expireAtMs =
      options?.expirationTtl === undefined
        ? undefined
        : now + options.expirationTtl * 1000;
    this.#append(key, {
      seq: this.#nextSeq(),
      writtenAtMs: now,
      value,
      ...(options?.metadata === undefined
        ? {}
        : { metadata: options.metadata }),
      ...(expireAtMs === undefined ? {} : { expireAtMs }),
      tombstone: false,
    });
  }

  #tombstone(key: string): void {
    if (this.#injectSet.has(key)) {
      throw new InjectedStoreError("delete", key);
    }
    this.#append(key, {
      seq: this.#nextSeq(),
      tombstone: true,
      writtenAtMs: this.#clock.now(),
    });
  }

  #append(key: string, version: Version): void {
    const history = this.#versions.get(key);
    if (history) {
      history.push(version);
    } else {
      this.#versions.set(key, [version]);
    }
  }

  // ── Chaos controls (world.chaos) ──────────────────────────────────────────

  /** Delay ALL of replica `index`'s reads by `ms` (models a lagging replica). */
  delayReplica(index: number, ms: number): void {
    this.#replicaDelayMs[index] = ms;
  }

  /** Delay every replica's visibility of `key` by `ms` ("delay one KV key"). */
  /** Chaos: set what a `set` costs in logical ms from here on (see `FakeStoreOptions.writeCostMs`).
   * 0 restores free writes. */
  setWriteCost(ms: number): void {
    if (ms < 0) {
      throw new Error(`FakeStore.setWriteCost: ms must be >= 0 (got ${ms})`);
    }
    this.#writeCostMs = ms;
  }

  delayKey(key: string, ms: number): void {
    this.#keyDelayMs.set(key, ms);
  }

  /** Force the next write to `key` to throttle (429). */
  throttleKey(key: string): void {
    this.#forceThrottleKeys.add(key);
  }

  /**
   * Make reads of `key` throw until cleared (KV read error).
   *
   * DO NOT "make this realistic" by classifying what it throws (§8, E13). The rejection here is
   * UNCLASSIFIED ON PURPOSE, and it is the ONLY subject this lane has for the kernel's
   * rethrow-unclassified path — the backstop that exists so a driver which fails to classify is
   * LOUD instead of silently laundering its own bugs into misses. Teach this fake to classify and
   * that path keeps every test green while covering nothing, which is how a branch gets orphaned:
   * the behaviour stays right, the teeth quietly stop reaching it.
   *
   * Classification is the DRIVER's job and belongs where a real driver does it — inside the tier's
   * own `get`. The conformance fixtures carry a read fault hand that classifies from inside, which
   * is the honest analogue of `throttleKey` above; this one is its deliberate opposite. The split is
   * permanent and both halves are load-bearing.
   */
  injectGetFailure(key: string): void {
    this.#injectGet.add(key);
  }

  /** Make writes/deletes of `key` throw until cleared (KV write error). */
  injectSetFailure(key: string): void {
    this.#injectSet.add(key);
  }

  clearInjections(): void {
    this.#forceThrottleKeys.clear();
    this.#injectGet.clear();
    this.#injectSet.clear();
  }
}
