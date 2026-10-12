/* References: §8, E13. */

// PLATFORM: this file lives under `src/adapters/**`, the sanctioned platform boundary

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
 * KV's per-value ceiling (§8 driver table): 25 MiB. An over-limit `set` is REJECTED
 * `too_large` before any I/O — never truncated, never written.
 */
const MAX_VALUE_BYTES = 25 * 1024 * 1024;

/**
 * KV's write-side minimum residency (§8 driver table): a `put` whose `expirationTtl`
 * is below 60s is silently raised to 60s by KV itself. The driver raises it explicitly
 * so the honored TTL is observable and matches what KV would do (D1). Distinct from the
 * read-side `cacheTtl` minimum (30s) — a different knob, not conflated here.
 */
const KV_MIN_EXPIRATION_TTL = 60;

/* References: §4.2. */

const READ_CACHE_TTL_S: Record<ReadKind, number> = {
  delta: 86_400,
  pointer: 30,
  snap: 86_400,
};

/**
 * A `StoreWriteError`-shaped rejection raised by the driver. A CLASS (not a bare
 * object) so oxlint's throw rules are satisfied; consumers discriminate it
 * STRUCTURALLY via `isStoreWriteError` (§8, E13), never by this class's identity, so it
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
 * STRUCTURALLY by the kernel (`isStoreReadError`, §8, E13) and never by identity, so it is never
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
 * lane — see DIVERGENCES D2/D14; the true production 429 is a C6 probe concern.)
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

/** Map a rejected KV write to the structural failure contract (§8, E13). */
function classifyWriteError(error: unknown): KvStoreWriteError {
  if (isRateLimited(error)) {
    // KV's 1 write/s/key ceiling. Transient ⇒ retryable; the kernel suppresses it to a
    // non-durable write-back (E13) rather than failing the caller.
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

/* References: §8, E13. */

function classifyReadError(error: unknown): KvStoreReadError {
  if (isRateLimited(error)) {
    // The tier's rate ceiling, surfaced as a 429. Transient ⇒ retryable; the kernel suppresses it
    // to a plain miss (E13) and the reader refills rather than failing the caller.
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

/** A `Store` (§8) backed by a single Cloudflare `KVNamespace` binding. */
class CloudflareKvStore implements Store {
  readonly #ns: KVNamespace;

  constructor(ns: KVNamespace) {
    this.#ns = ns;
  }

  async get(key: string, readKind?: ReadKind): Promise<StoreValue | undefined> {
    // PLATFORM: ambient KV read. `getWithMetadata` returns the stored bytes plus any
    /* References: §5.0, §4.2, E5, §1. */

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
    // Oversize FIRST, before any I/O (§8, E13): reject `too_large`, never truncate,
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
      // Floor a supplied `expirationTtl` to KV's 60s write-side minimum (D1). No
      // "+slack" term is added — none is defined in the spec/errata (D13). When absent,
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

/** Build a `Store` (§8) over a Cloudflare `KVNamespace` binding. */
export function cloudflareKV(ns: KVNamespace): Store {
  return new CloudflareKvStore(ns);
}
