import type { TagKnowledge } from "./knowledge";
import { safeEmit, TELEMETRY_EVENTS } from "./telemetry";
import type { SinkErrorHook, TelemetrySink } from "./telemetry";

/* References: §1. */

export interface Clock {
  /** Milliseconds on the logical timeline. Never a wall clock in the correctness path. */
  now(): number;
}

/** Injected deterministic RNG — the ONLY source of randomness in kernel code. */
export interface Rng {
  /** A float in [0, 1). Seeded and reproducible. */
  next(): number;
}

export type StoreMetadata = Readonly<Record<string, unknown>>;

/** A value read from / written to a KV-like store. */
export interface StoreValue {
  readonly value: string;
  readonly metadata?: StoreMetadata;
}

export interface StoreWriteOptions {
  /** Physical residency in seconds (§3: KV floors this at 60s). */
  readonly expirationTtl?: number;
  readonly metadata?: StoreMetadata;
}

/**
 * Which immutable mirror object a WRITE carries (§4.2, E19). The executor passes it to
 * `putImmutable` so a host maps kind → physical residency, NEVER by parsing the (E19-scoped) key —
 * the fourth-class "key edition" 2b-2-0 cured. The single home for the mirror-object taxonomy;
 * `@astilba/cache/registry` re-exports it.
 */
export type ImmutableKind = "delta" | "snap";

/**
 * The mirror-object intent a READ carries (§4.2) — the write taxonomy PLUS the mutable `pointer`
 * (`__state`). `Store.get`'s optional hint, symmetric with `putImmutable`: intent travels as a
 * PARAMETER so a driver maps kind → read-cache policy, never by parsing the key. One vocabulary for
 * mirror-object intent in both directions.
 */
export type ReadKind = "pointer" | ImmutableKind;

/**
 * A KV-like tier driver (§8). `memory` (L1, isolate-local) and `cloudflareKV` /
 * `redis` (L2, durable) implement it. The harness `FakeStore` models N replica
 * views with per-key visibility delay, per-key write throttle (1 write/s → 429),
 * TTL, and injectable get/set failures; a single-view `FakeStore` is the
 * strongly-consistent Redis stand-in.
 */
export interface Store {
  /* References: §4.2, §1. */

  get(key: string, readKind?: ReadKind): Promise<StoreValue | undefined>;
  set(key: string, value: string, options?: StoreWriteOptions): Promise<void>;
  delete(key: string): Promise<void>;
}

/**
 * The structural failure contract a `Store` driver raises to the kernel on a rejected
 * write (§8, E13). It is a plain SHAPE, never a class: the kernel discriminates on
 * `code` alone (`isStoreWriteError`) so a fake's throttle error and a real driver's
 * classified rejection speak ONE contract WITHOUT sharing a class — cross-realm and
 * cross-package `instanceof` identity is exactly the trap that makes a shared error
 * class unshippable. The kernel absorbs a classified write failure into an outcome +
 * telemetry (E13); a caller never sees the raw rejection.
 *
 *   - `throttled`   — the write hit the tier's per-key write-rate ceiling (KV's
 *                     1 write/s/key, §8). Transient: `retryable` is true.
 *   - `too_large`   — the value exceeds the tier's max value size (KV's 25 MiB
 *                     ceiling, §8). Typed and rejected, NEVER truncated; not retryable.
 *   - `unavailable` — a transient tier failure (the durable store was momentarily
 *                     unreachable, §8).
 */
export interface StoreWriteError {
  readonly code: "throttled" | "too_large" | "unavailable";
  readonly retryable: boolean;
  readonly cause?: unknown;
}

/**
 * Structural type-guard for `StoreWriteError` (§8, E13). Purely shape-based — it NEVER
 * uses `instanceof`, so a harness fake and a real driver are recognized by the SAME
 * check without a shared class. Total and safe on arbitrary `unknown`: rejects
 * non-objects/null, requires `code` to be exactly one of the three literals and
 * `retryable` to be a boolean.
 */
export function isStoreWriteError(e: unknown): e is StoreWriteError {
  if (typeof e !== "object" || e === null) {
    return false;
  }
  const code: unknown = "code" in e ? e.code : undefined;
  const retryable: unknown = "retryable" in e ? e.retryable : undefined;
  return (
    (code === "throttled" || code === "too_large" || code === "unavailable") &&
    typeof retryable === "boolean"
  );
}

/**
 * The structural failure contract a `Store` driver raises to the kernel on a rejected
 * READ (§8, E13) — the GET-side twin of `StoreWriteError`, and a plain SHAPE for the same
 * reason: the kernel discriminates on `code` alone (`isStoreReadError`), so a fake's
 * injected failure and a real driver's classified rejection speak ONE contract WITHOUT
 * sharing a class (cross-realm `instanceof` is the trap that makes a shared class
 * unshippable). A classified read failure is absorbed into a plain MISS (E13); an
 * UNCLASSIFIED rejection is never absorbed — it propagates to the caller unchanged.
 *
 * Deliberately NOT re-exported from the barrel: the read contract is kernel-internal. `cloudflareKV`
 * raises it — classifying a rejected KV read at the platform boundary, which is the only place that
 * knows what a KV fault looks like — and imports it from here by module path, as the Store
 * conformance contract does.
 *
 *   - `throttled`   — the read hit the tier's rate ceiling (a 429). Transient.
 *   - `unavailable` — a transient tier failure (the store was momentarily unreachable, §8).
 *
 * There is no read-side `too_large`: a size ceiling is a property of what a WRITE offers,
 * and a read that returns bytes has already cleared it.
 */
export interface StoreReadError {
  readonly code: "throttled" | "unavailable";
  readonly retryable: boolean;
  readonly cause?: unknown;
}

/**
 * Structural type-guard for `StoreReadError` (§8, E13) — shape-based, never `instanceof`,
 * exactly as `isStoreWriteError` is. Total and safe on arbitrary `unknown`. A driver error
 * that carries a read-suppressible `code` satisfies this whatever class raised it, which is
 * the point: E13's own `unavailable` shape is recognized without a shared identity.
 */
export function isStoreReadError(e: unknown): e is StoreReadError {
  if (typeof e !== "object" || e === null) {
    return false;
  }
  const code: unknown = "code" in e ? e.code : undefined;
  const retryable: unknown = "retryable" in e ? e.retryable : undefined;
  return (
    (code === "throttled" || code === "unavailable") &&
    typeof retryable === "boolean"
  );
}

/**
 * Ordering key an atomic compare-and-set must never regress (§5.0, E3): epoch first,
 * then fence. A born-monotone write-back is arbitrated on exactly this pair (INV-17).
 */
export interface CasOrder {
  readonly epoch: number;
  readonly fence: number;
}

/** A versioned CAS slot: `token` is the optimistic-concurrency version. */
export interface CasRecord {
  readonly token: number;
  readonly order: CasOrder;
}

/**
 * The atomic compare-and-set a CAS-capable durable driver (Redis Lua, DO
 * transactional storage) layers ON TOP OF the base `Store` (§5.0, E3). An OPTIONAL
 * capability: the base `Store` — and the v0.1 fakes — expose none, so the kernel's
 * floor stays read-compare-write (INV-17) and prefer-CAS consumption is deferred to
 * the first real CAS driver. Declared here so the conformance layer can pin the
 * capability and a real driver can later satisfy the same shape.
 */
export interface CasStore {
  seed(key: string, order: CasOrder): Promise<void>;
  load(key: string): Promise<CasRecord | undefined>;
  /** Apply `next` iff the stored token still equals `expectedToken`; else a lost race. */
  swap(
    key: string,
    expectedToken: number,
    next: CasOrder
  ): Promise<number | undefined>;
}

/** Acknowledgement of a durably-accepted registry mutation (§4.3 step 1). */
export interface RegistryAck {
  /** The Coordinator epoch after applying this mutation. */
  readonly epoch: number;
}

/**
 * The authoritative registry / Coordinator (§4.1–§4.3). Holds dual watermark
 * epochs per tag (monotone, max-on-write) and a persisted monotone epoch counter.
 * `check` is the live, un-memoized strong read used by strong reads and the
 * write-back fence (E1). `doRegistry` / `redisRegistry` implement it in phase 2;
 * the harness `FakeRegistry` is the fake.
 */
export interface Registry {
  /** The registry id scoping this Coordinator's §4.2 mirror keyspace (E19). The per-isolate
   * reader scopes the keys it resyncs off to this value AND asserts the pointer payload echoes
   * it — a mismatch reads an empty keyspace and fails closed. `doRegistry`/`FakeRegistry`
   * default it to `"default"`; the authoring channel is `configure` (env `REGISTRY_ID`). */
  readonly regId: string;
  /** Live watermark knowledge for these tags (E1). */
  check(tags: readonly string[]): Promise<readonly TagKnowledge[]>;
  /** Soft purge: max-on-write bump of `softEpoch` (§2.2). */
  expire(tags: readonly string[]): Promise<RegistryAck>;
  /** Hard purge: max-on-write bump of `hardEpoch` (§2.2). */
  delete(tags: readonly string[]): Promise<RegistryAck>;
  /** Register this instance's retention lease at bind (§4.5, E11). */
  registerRetention(retentionMs: number): Promise<void>;
}

/** One tag's watermark change within a replication frame (§4.2). */
export interface TagChange {
  readonly tag: string;
  readonly softEpoch?: number;
  readonly hardEpoch?: number;
}

/** A bus frame carrying a contiguous epoch range of changes (§4.2, M2). */
export interface BusFrame {
  readonly fromEpoch: number;
  readonly toEpoch: number;
  readonly changes: readonly TagChange[];
}

/**
 * One event delivered to a bus subscriber (§4.2, E4/E28). The transport hands the
 * subscriber one of these discriminated shapes; epoch contiguity is validated by the
 * KERNEL, never asserted by the bus (B3 — the bus transports, it does not vouch):
 *
 *   - `frame`: a replication frame carrying its own `{fromEpoch,toEpoch}` range.
 *   - `gap`:   a frame was dropped or lost in transit — the subscriber must treat its
 *              knowledge as suspect and resync before warm trust resumes. Carries `head`
 *              (B2′, E21): a lower bound ≥ the `toEpoch` of every frame emitted before the
 *              signal, so the loss-revealing event ITSELF declares the head the reader must
 *              reach to account for what it missed (detection carries its own remedy).
 *   - `reset`: the transport was (re)established — emitted BEFORE any frame on every
 *              establishment (B1); it suspends all warm trust until a resync
 *              handshake reconciles the subscriber's applied epoch (E4).
 *   - `hello`: the live channel declares its current head epoch — emitted right AFTER
 *              `reset` on every establishment, before any frame (B1′, E21/E4). Convergence
 *              anchors to a head DECLARED (`hello`/`gap`/a non-contiguous frame's `toEpoch`),
 *              never to pointer freshness; an idle channel's `hello` head equals the applied
 *              epoch, so a caught-up subscriber reconverges at once.
 *   - `lost`:  the transport is DOWN — a dial that did not come up, or an established socket
 *              that closed (E28). It is the counterpart of `reset`, and it exists because its
 *              absence is unreadable: with only `reset`/`hello` on the wire, a channel that never
 *              connected and a healthy channel with nothing to say produce the SAME stream —
 *              none. A subscriber cannot distinguish silence-because-quiet from
 *              silence-because-dead by waiting longer, so the transport declares it instead.
 *              It carries no epoch: a channel that is down declares nothing ABOUT epochs, and a
 *              `head` here would be a number invented by the thing that just failed. It does NOT
 *              raise suspicion — E20/E21 own that posture and a down channel is not a gapped map
 *              (a never-established reader is DECLARED degraded, which is a different, honest
 *              state); it is tracked and surfaced, and what holds the reader correct in the
 *              meantime is the polling driver plus its refusal to guess at a tag it cannot
 *              establish.
 */
export type BusEvent =
  | { readonly kind: "frame"; readonly frame: BusFrame }
  | { readonly kind: "gap"; readonly head: number }
  | { readonly kind: "reset" }
  | { readonly kind: "hello"; readonly head: number }
  | { readonly kind: "lost" };

export interface Subscription {
  close(): void;
}

/**
 * Cross-instance invalidation bus (§4.2, E4). A subscriber consumes a `BusEvent`
 * stream — frames plus the out-of-band `gap`/`reset` signals — and the KERNEL, not
 * the bus, asserts frame contiguity and forces a resync on any gap or reset (M2/E4).
 * `doBus` / `redisBus` implement it in phase 2; the harness `FakeBus` models drop,
 * delay, reorder, and disconnect/reconnect over this same union.
 */
export interface Bus {
  subscribe(onEvent: (event: BusEvent) => void): Subscription;
}

/** CDN purge acceptance modes for `delete` (§2.2). */
export type CdnMode = "enqueue" | "await" | "block";

/**
 * CDN / L3 purge driver (§4.3 step 4). Its own persisted drain queue with its own
 * alarm; acceptance surfaces via `edgePurged()` (MI9/MI10). `cloudflarePurge`
 * implements it in phase 2.
 */
export interface Cdn {
  purge(tags: readonly string[], mode: CdnMode): Promise<void>;
}

/**
 * A pluggable value codec (§8). `id` is the codec name + wire version — the
 * `Envelope.codecId` source of truth (E8). A decode failure or an `id` mismatch
 * (not in `acceptCodecs`) fails closed to a miss (invariants 8, 27).
 */
export interface Codec {
  readonly id: string;
  encode(value: unknown): string;
  decode(value: string): unknown;
}

/** A held cross-isolate lock carrying a monotonic fencing token (§5.2, E3). */
export interface LockHandle {
  /**
   * Monotonic fencing token — the E3 tie-breaker used above an equal `bornEpoch`
   * and always above `bornMs`; a locked fill carries it into `Envelope.fence`.
   */
  readonly fence: number;
  release(): Promise<void>;
}

/**
 * Opt-in cross-isolate lock (§5.2) — DO or Redis primary only. Grants mutual
 * exclusion plus a monotonic fencing token; the born-monotone write-back rule
 * (§5.0, E3) is what bounds a stale lease-holder's late write. `doRegistry` /
 * `redis` back it in phase 2; the harness `FakeLock` is the fake.
 */
export interface Lock {
  acquire(key: string): Promise<LockHandle>;
}

/* References: §8, E23. */

export interface MemoryOptions {
  /** Injected logical clock (§1(3)) — the ONLY `now` source; required to honor `expirationTtl`. */
  readonly clock?: Clock;
  readonly maxEntries?: number;
  readonly maxBytes?: number;
  /** MI12 private-eviction disclosure sink (§10, CACHE-21). Optional; off by default. */
  readonly telemetry?: TelemetrySink;
  /** Injectable broken-sink hook `safeEmit` calls when it swallows a `telemetry` failure (D4). */
  readonly onSinkError?: SinkErrorHook;
}

/** One resident L1 entry: the stored bytes + accounting the LRU/TTL/bounds logic needs. */
interface MemoryEntry {
  value: string;
  metadata?: StoreMetadata;
  /** The value's UTF-8 byte length — summed into the per-instance byte accounter. */
  bytes: number;
  /** Absolute expiry on the injected clock's timeline (`clock.now() + ttl*1000`); absent ⇒ no TTL. */
  expireAtMs?: number;
}

/**
 * Rejection raised by `memory().set` when a single value's UTF-8 byte length exceeds the
 * store's total `maxBytes` budget — a value larger than the whole budget can never fit, so
 * it is REJECTED, never truncated (§8, E13). Satisfies the structural `StoreWriteError`
 * contract (`too_large`, non-retryable) WITHOUT sharing a class with the kernel: consumers
 * discriminate via `isStoreWriteError` (structural), never `instanceof`. The kernel-side
 * twin of the harness `OversizeValueError`; internal, so it needs no export.
 */
class MemoryOversizeError extends Error implements StoreWriteError {
  readonly code = "too_large";
  readonly retryable = false;

  constructor(key: string, sizeBytes: number, maxBytes: number) {
    super(`value too large: ${key} (${sizeBytes} > ${maxBytes} bytes)`);
    this.name = "MemoryOversizeError";
  }
}

/**
 * Rejection raised by `memory().set` when a write carries `expirationTtl` but the store was built
 * with NO injected clock — the residency cannot be honored without a `now` source, so the write
 * FAILS LOUD rather than silently drop the TTL (§8). Unlike `MemoryOversizeError` this is a
 * MISCONFIGURATION, not a `StoreWriteError`: it must NOT satisfy the structural write-failure
 * contract the kernel absorbs (E13) — a residency the store can never honor is a programmer error,
 * surfaced typed so a caller can `instanceof`-catch it, never quietly ignored. Internal, mirroring
 * `MemoryOversizeError` (neither is re-exported from `index.ts`).
 */
class MemoryConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "MemoryConfigError";
  }
}

/** MI12 (§6, §10, CACHE-21): is this a PRIVATE (`usr:`-scoped) L1 entry? Read off the `scope`
 * metadata LITERAL the kernel writes with every L1 set (`pub` / `usr:<h>` / `ten:<h>`, §3) — the
 * `usr:` PREFIX only, never the hash. A `usr:` entry is per-principal, so its eviction is disclosed;
 * detection touches nothing identifying, and a non-canonical store (no `scope` metadata) never
 * matches. */
function isPrivateEntry(entry: MemoryEntry): boolean {
  const scope = entry.metadata?.scope;
  return typeof scope === "string" && scope.startsWith("usr:");
}

/**
 * In-process L1 store (§8) — isolate-local, bounded by `maxEntries` / `maxBytes` with LRU
 * eviction on either bound (an insertion-ordered `Map`, re-inserted on touch, so recency —
 * not mere insertion order — picks the victim). Each `memory()` owns a PER-INSTANCE byte
 * accounter (Σ value UTF-8 byte lengths).
 *
 * MI12 (§6, CACHE-21): an optional `telemetry` sink receives a `private_evicted` event when a
 * PRIVATE (`usr:`-scoped) entry is evicted from the LRU under memory pressure — reason class +
 * counts + bytes ONLY, so an operator sees L1 pressure on private data without the event ever
 * naming WHAT was evicted (E23 payload discipline). The `usr:`-priority-protected LRU (evict
 * private entries LAST) is a spec MAY and stays UNBUILT: this discloses whatever the current LRU
 * policy evicts, it does not change the victim. The MI11 cross-instance shared/joint reserve (warn
 * on Σ`maxBytes` overflow across isolate L1s) is a separate advisory extra, still DEFERRED.
 */
export function memory(options?: MemoryOptions): Store {
  const clock = options?.clock;
  const maxEntries = options?.maxEntries;
  const maxBytes = options?.maxBytes;
  const telemetry = options?.telemetry;
  const onSinkError = options?.onSinkError;
  const encoder = new TextEncoder();
  // Insertion order doubles as LRU recency: the first key in iteration order is the
  // least-recently-used; delete+re-set on a touch moves a key to the MRU end.
  const entries = new Map<string, MemoryEntry>();
  let totalBytes = 0;

  const drop = (key: string, entry: MemoryEntry): void => {
    entries.delete(key);
    totalBytes -= entry.bytes;
  };
  // Over EITHER configured bound? (`totalBytes` is closure-mutated by `drop`, so this is a
  // helper rather than an inline loop condition — the latter reads as never-modified.)
  const overBounds = (): boolean =>
    (maxEntries !== undefined && entries.size > maxEntries) ||
    (maxBytes !== undefined && totalBytes > maxBytes);

  return {
    delete: async (key): Promise<void> => {
      const entry = entries.get(key);
      if (entry !== undefined) {
        drop(key, entry);
      }
    },
    get: async (key): Promise<StoreValue | undefined> => {
      const entry = entries.get(key);
      const expired =
        entry !== undefined &&
        clock !== undefined &&
        entry.expireAtMs !== undefined &&
        clock.now() >= entry.expireAtMs;
      if (entry === undefined || expired) {
        // Absent, or (lazily) reclaim an expired entry on read — treat as a miss.
        if (entry !== undefined) {
          drop(key, entry);
        }
        return undefined;
      }
      // Touch: re-insert at the MRU end so a later read protects it from LRU eviction.
      entries.delete(key);
      entries.set(key, entry);
      return entry.metadata === undefined
        ? { value: entry.value }
        : { metadata: entry.metadata, value: entry.value };
    },
    set: async (key, value, writeOptions): Promise<void> => {
      const bytes = encoder.encode(value).length;
      if (maxBytes !== undefined && bytes > maxBytes) {
        // Reject BEFORE mutating: a prior value at `key` survives byte-identical (§8, E13).
        throw new MemoryOversizeError(key, bytes, maxBytes);
      }
      let expireAtMs: number | undefined;
      if (writeOptions?.expirationTtl !== undefined) {
        if (clock === undefined) {
          throw new MemoryConfigError(
            "memory(): expirationTtl requires an injected clock — construct memory({ clock, … }) to honor residency (§8)"
          );
        }
        expireAtMs = clock.now() + writeOptions.expirationTtl * 1000;
      }
      const existing = entries.get(key);
      if (existing !== undefined) {
        // Replace: drop the old byte-count and slot so the re-insert lands at the MRU end.
        drop(key, existing);
      }
      entries.set(key, {
        bytes,
        value,
        ...(writeOptions?.metadata === undefined
          ? {}
          : { metadata: writeOptions.metadata }),
        ...(expireAtMs === undefined ? {} : { expireAtMs }),
      });
      totalBytes += bytes;
      // Evict least-recently-used until within BOTH bounds. The just-set key is MRU
      // (last), so it is the last candidate and — having passed the oversize gate — can
      // never be its own victim. `overBounds()` ⇒ at least one entry, so the LRU lookup
      // always finds one; the guard only satisfies the optional-value type.
      while (overBounds()) {
        const lruKey = entries.keys().next().value;
        if (lruKey === undefined) {
          break;
        }
        const lru = entries.get(lruKey);
        if (lru !== undefined) {
          drop(lruKey, lru);
          if (telemetry !== undefined && isPrivateEntry(lru)) {
            // MI12: disclose that a PRIVATE entry left L1. Reason class + counts + bytes ONLY —
            // never a key/hash/tag (E23 payload discipline). One event per evicted `usr:` entry.
            safeEmit(
              telemetry,
              {
                bytes: lru.bytes,
                count: 1,
                type: TELEMETRY_EVENTS.private_evicted,
              },
              onSinkError
            );
          }
        }
      }
    },
  };
}
