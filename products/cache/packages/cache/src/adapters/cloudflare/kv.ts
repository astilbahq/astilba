// @astilba/cache adapter — the `cloudflareKV` Store driver (phase 2).
//
// PLATFORM: this file lives under `src/adapters/**`, the sanctioned platform boundary
//. It talks to a workerd `KVNamespace` binding and uses the ambient
// `TextEncoder` global — legal HERE, on the boundary, never in the kernel core. It is
// typechecked under the integration lane's Workers-typed program (the package tsconfig
// excludes this subtree); the kernel core stays pure.
//
// THE THIN SHELL. A `Store` over a single `KVNamespace`. It owns no cache
// semantics — it marshals `get`/`set`/`delete` to the binding and classifies a rejected
// write or read into the STRUCTURAL `StoreWriteError` / `StoreReadError` shapes
// the kernel already speaks. No compare-and-set: KV declares none, so the
// kernel's read-compare-write stays the floor; this driver implements the base `Store` only.
//
// WHY CLASSIFICATION IS THE DRIVER'S JOB AND NOT THE KERNEL'S. The convention has two halves and
// only works with both. The ADAPTER classifies at the platform boundary, because platform knowledge
// lives here — this file is the only one in the tree that knows what a KV 429 looks like. The KERNEL
// then absorbs what a driver NAMED and rethrows what nobody named, as a non-conformance signal: a
// kernel that swallowed unknowns would turn every future driver defect into a silent miss, failing
// open on exactly the faults nobody anticipated. So an adapter that classifies nothing does not get
// the kernel's tolerance — it gets the kernel's alarm, on every real failure. Classify here.

import type {
  ReadKind,
  Store,
  StoreMetadata,
  StoreReadError,
  StoreValue,
  StoreWriteError,
  StoreWriteOptions,
} from "../../drivers";

/**
 * KV's per-value ceiling (driver table): 25 MiB. An over-limit `set` is REJECTED
 * `too_large` before any I/O — never truncated, never written.
 */
const MAX_VALUE_BYTES = 25 * 1024 * 1024;

/**
 * KV's write-side minimum residency (driver table): a `put` whose `expirationTtl`
 * is below 60s is silently raised to 60s by KV itself. The driver raises it explicitly
 * so the honored TTL is observable and matches what KV would do. Distinct from the
 * read-side `cacheTtl` minimum (30s) — a different knob, not conflated here.
 */
const KV_MIN_EXPIRATION_TTL = 60;

/**
 * Read-cache `cacheTtl` per mirror-object kind, in SECONDS. The mutable `pointer`
 * (`__state`) takes KV's pinned read-cache MINIMUM (30s); the immutable `delta`/`snap` cache hard
 * (24h — cache the immutable, never the mutable). An UNHINTED read passes no `cacheTtl` and inherits
 * KV's 60s edge-cache default (NOT minimal caching — the Appendix C trap this wiring closes).
 */
const READ_CACHE_TTL_S: Record<ReadKind, number> = {
  delta: 86_400,
  pointer: 30,
  snap: 86_400,
};

/**
 * A `StoreWriteError`-shaped rejection raised by the driver. A CLASS (not a bare
 * object) so oxlint's throw rules are satisfied; consumers discriminate it
 * STRUCTURALLY via `isStoreWriteError`, never by this class's identity, so it
 * is never exported. `cause` carries the original KV rejection where there is one.
 */
class KvStoreWriteError extends Error implements StoreWriteError {
  readonly code: StoreWriteError["code"];
  readonly retryable: boolean;

  constructor(
    code: StoreWriteError["code"],
    retryable: boolean,
    message: string,
    cause?: unknown
  ) {
    super(message, cause === undefined ? undefined : { cause });
    this.name = "KvStoreWriteError";
    this.code = code;
    this.retryable = retryable;
  }
}

/**
 * A `StoreReadError`-shaped rejection raised by the driver — the GET-side twin of
 * `KvStoreWriteError`, and a CLASS for the same reason (oxlint's throw rules), recognized
 * STRUCTURALLY by the kernel (`isStoreReadError`) and never by identity, so it is never
 * exported. `cause` carries the original KV rejection.
 */
class KvStoreReadError extends Error implements StoreReadError {
  readonly code: StoreReadError["code"];
  readonly retryable: boolean;

  constructor(
    code: StoreReadError["code"],
    retryable: boolean,
    message: string,
    cause: unknown
  ) {
    super(message, { cause });
    this.name = "KvStoreReadError";
    this.code = code;
    this.retryable = retryable;
  }
}

/**
 * Detect a KV rate-limit (429) rejection DEFENSIVELY. KV enforces 1 write/s/key and
 * surfaces the ceiling as a 429; workerd throws that as an ordinary `Error` with no
 * documented, stable structured field. So probe a few shapes WITHOUT assuming any
 * exists — a numeric `status`/`statusCode`/`code` equal to 429, or a message that names
 * the 429 / rate-limit condition — and treat everything unrecognized as a transient
 * `unavailable` rather than mis-tagging it `throttled`. (Miniflare emits no real 429, so
 * this branch is proven by the deterministic conformance fixture, not the integration
 * lane — see DIVERGENCES true production 429 is a probe concern.)
 */
function isRateLimited(error: unknown): boolean {
  if (typeof error === "object" && error !== null) {
    if ("status" in error && error.status === 429) {
      return true;
    }
    if ("statusCode" in error && error.statusCode === 429) {
      return true;
    }
    if ("code" in error && error.code === 429) {
      return true;
    }
  }
  const message = error instanceof Error ? error.message : "";
  return /\b429\b|too many requests|rate ?limit/iu.test(message);
}

/** Map a rejected KV write to the structural failure contract. */
function classifyWriteError(error: unknown): KvStoreWriteError {
  if (isRateLimited(error)) {
    // KV's 1 write/s/key ceiling. Transient ⇒ retryable; the kernel suppresses it to a
    // non-durable write-back rather than failing the caller.
    return new KvStoreWriteError(
      "throttled",
      true,
      "cloudflareKV: write throttled (KV 1 write/s/key ceiling, 429)",
      error
    );
  }
  // Any other transient KV write failure: the durable store was momentarily
  // unreachable. Retryable, and classified so the kernel absorbs it into telemetry.
  return new KvStoreWriteError(
    "unavailable",
    true,
    "cloudflareKV: write failed (durable store unavailable)",
    error
  );
}

/**
 * Map a rejected KV read to the structural failure contract. TOTAL, which is the whole
 * design: the posture is that an L2 read failure of ANY shape is a miss-class event, so there is
 * no rejection this function declines to name. `isRateLimited` is reused verbatim — the same 429 the
 * write half recognizes, since it is the same platform saying the same thing — and everything else
 * lands on transient `unavailable`.
 *
 * The totality is not tidiness. A partial classifier would name the shapes someone already thought
 * of and drop the rest through the kernel's rethrow to the caller — and the rest is precisely the
 * interesting set: the production faults nobody wrote a recognizer for. Read-side, over-naming costs
 * an origin refill; under-naming costs the request. There is no read-side `too_large`: a size
 * ceiling is a property of what a WRITE offers.
 */
function classifyReadError(error: unknown): KvStoreReadError {
  if (isRateLimited(error)) {
    // The tier's rate ceiling, surfaced as a 429. Transient ⇒ retryable; the kernel suppresses it
    // to a plain miss and the reader refills rather than failing the caller.
    return new KvStoreReadError(
      "throttled",
      true,
      "cloudflareKV: read throttled (KV rate ceiling, 429)",
      error
    );
  }
  // Everything else: the durable store did not answer. Transient, named, and absorbed — the caller
  // pays an origin read, never the error.
  return new KvStoreReadError(
    "unavailable",
    true,
    "cloudflareKV: read failed (durable store unavailable)",
    error
  );
}

/** A `Store` backed by a single Cloudflare `KVNamespace` binding. */
class CloudflareKvStore implements Store {
  readonly #ns: KVNamespace;

  constructor(ns: KVNamespace) {
    this.#ns = ns;
  }

  async get(key: string, readKind?: ReadKind): Promise<StoreValue | undefined> {
    // PLATFORM: ambient KV read. `getWithMetadata` returns the stored bytes plus any
    // metadata written alongside them (the born-monotone envelope carries bornEpoch/
    // fence in metadata) so a read is complete, not value-only.
    //
    // Read-cache hint: map the mirror-object intent to `cacheTtl`. OMITTING the
    // option is NOT minimal caching — it inherits KV's 60s edge-cache DEFAULT (Appendix C trap),
    // which would stack with the 60s poll (doubling the staleness bound) and delay every silence
    // observation. The one MUTABLE object, `pointer`, takes the pinned 30s minimum; the IMMUTABLE
    // `delta`/`snap` cache hard (24h). An UNHINTED read (entry reads, allowed eventual) passes nothing
    // and keeps the platform default. Enforcement is Miniflare-blind; the selected value is
    // the observable, its achieved distribution a probe.
    const cacheTtl =
      readKind === undefined ? undefined : READ_CACHE_TTL_S[readKind];
    let result: KVNamespaceGetWithMetadataResult<string, StoreMetadata>;
    try {
      result = await this.#ns.getWithMetadata<StoreMetadata>(
        key,
        cacheTtl === undefined ? undefined : { cacheTtl }
      );
    } catch (error) {
      throw classifyReadError(error);
    }
    if (result.value === null) {
      return undefined;
    }
    return result.metadata === null
      ? { value: result.value }
      : { metadata: result.metadata, value: result.value };
  }

  async set(
    key: string,
    value: string,
    options?: StoreWriteOptions
  ): Promise<void> {
    // Oversize FIRST, before any I/O: reject `too_large`, never truncate,
    // never touch KV. `TextEncoder`, never `Buffer` — `Buffer` is not a workerd global
    // (no `nodejs_compat`); UTF-8 byte length is the measure KV enforces.
    // PLATFORM: ambient `TextEncoder`.
    const sizeBytes = new TextEncoder().encode(value).length;
    if (sizeBytes > MAX_VALUE_BYTES) {
      throw new KvStoreWriteError(
        "too_large",
        false,
        `cloudflareKV: value too large (${sizeBytes} > ${MAX_VALUE_BYTES} bytes)`
      );
    }
    const putOptions: KVNamespacePutOptions = {
      // Floor a supplied `expirationTtl` to KV's 60s write-side minimum. No
      // "+slack" term is added — none is defined in the design. When absent,
      // no expiration is set (the residency is whatever the caller/kernel supplies).
      ...(options?.expirationTtl === undefined
        ? {}
        : {
            expirationTtl: Math.max(
              KV_MIN_EXPIRATION_TTL,
              Math.ceil(options.expirationTtl)
            ),
          }),
      ...(options?.metadata === undefined
        ? {}
        : { metadata: options.metadata }),
    };
    try {
      // PLATFORM: ambient KV write.
      await this.#ns.put(key, value, putOptions);
    } catch (error) {
      throw classifyWriteError(error);
    }
  }

  async delete(key: string): Promise<void> {
    // PLATFORM: ambient KV delete.
    await this.#ns.delete(key);
  }
}

/** Build a `Store` over a Cloudflare `KVNamespace` binding. */
export function cloudflareKV(ns: KVNamespace): Store {
  return new CloudflareKvStore(ns);
}
