// Public error surface: RegistryUnavailableError on strong reads),
// CacheTimeoutError{source}. NotImplementedError is the kernel-phase stub
// sentinel: every public entry point throws it until the invariant suite is
// reviewed and implementation begins.

/**
 * Thrown by every public entry point during the correctness-kernel phase. A RED
 * invariant that fails with `NotImplementedError` is red for the right reason
 * (the kernel is unimplemented), as opposed to a toolchain/config failure.
 */
export class NotImplementedError extends Error {
  constructor(symbol: string) {
    super(`${symbol}: not implemented in the correctness-kernel phase`);
    this.name = "NotImplementedError";
  }
}

/**
 * The single kernel-phase stub body. Every public function/method delegates here
 * so the switch to real logic is a body replacement with zero signature churn.
 * Callers pass their arguments through so parameters are genuinely referenced;
 * inside an `async` method the thrown error becomes a rejected promise.
 */
export function notImplemented(
  symbol: string,
  ..._args: readonly unknown[]
): never {
  throw new NotImplementedError(symbol);
}

/** Whether a cache timeout originated inside the cache or from a caller abort. */
export type CacheTimeoutSource = "cache" | "caller";

/**
 * Distinguishes cache-originated timeouts (retriable — evidence about origin
 * health) from caller aborts (not evidence of origin health).
 */
export class CacheTimeoutError extends Error {
  readonly source: CacheTimeoutSource;

  constructor(source: CacheTimeoutSource, message?: string) {
    super(message ?? `cache operation timed out (source: ${source})`);
    this.name = "CacheTimeoutError";
    this.source = source;
  }
}

/**
 * Thrown by a `strong` read when the registry is unreachable. Opt out with
 * `onUnavailable: 'eventual'`.
 */
export class RegistryUnavailableError extends Error {
  /**
   * `options.cause` carries the error this one displaced, where there was one. The serve-time
   * re-validation of a graced candidate runs INSIDE a failed fill: if the registry is unreachable
   * there, this error is what the caller sees instead of the factory failure that started it, so
   * the factory failure rides along rather than being dropped. Nothing is lost and callers get one
   * uniform type. Widened additively — `new RegistryUnavailableError(msg)` is unchanged.
   */
  constructor(message?: string, options?: ErrorOptions) {
    super(message ?? "registry unreachable", options);
    this.name = "RegistryUnavailableError";
  }
}

/**
 * Thrown by a read whose tag knowledge is UNKNOWN under `unknownPolicy: 'error'`.
 *
 * WHAT MAKES THIS DISTINCT FROM A MISS, AND WHY THE DISTINCTION IS THE POINT. Every other
 * fail-closed resolution refills from origin, which is hard-safe exactly when the origin is truth.
 * `'error'` is the policy for the case where it is NOT: a namespace under a takedown or a legal
 * embargo. There, an origin that has not caught up yet can lose a race with the purge and hand back
 * exactly the content that was supposed to vanish. So this policy declines to guess: it neither
 * serves the unknown value nor replaces it
 * with a fresh one, and the caller — who knows what the content is — decides. A policy that resolved
 * to the same refill as `'miss'` would be a switch that reads as protection and does nothing, which
 * is worse than no switch at all.
 *
 * Carries the key and the tags whose knowledge was unknown, so a caller can tell an embargoed
 * dependency from a cold one. EXACTLY those: the tags this read could not establish, and no others.
 * A payload of everything the entry declared cannot answer the question the field exists for — the
 * caller would have to guess which of them was the problem, which is the guess this policy refuses
 * to make on their behalf. Reserved (`__`-prefixed) tags never appear here: they are the kernel's
 * own bookkeeping, they are unknown for exactly the same reason the user's are, and naming them
 * tells the caller nothing they can act on.
 */
export class UnknownTagError extends Error {
  readonly key: string;
  readonly tags: readonly string[];

  /**
   * `armedBy` names what selected this resolution — an explicit `unknownPolicy: 'error'`, or the
   * `takedownSensitive` override that outranks it. The message used to state the former
   * unconditionally, which was wrong precisely on the takedown path: the one place where the
   * operator most needs to know which switch fired is the one where the message named the other.
   */
  constructor(
    key: string,
    tags: readonly string[],
    armedBy: "unknownPolicy" | "takedownSensitive" = "unknownPolicy"
  ) {
    const cause =
      armedBy === "takedownSensitive"
        ? "takedownSensitive is set"
        : "unknownPolicy is 'error'";
    super(
      `read for key ${JSON.stringify(key)} has unknown tag knowledge and ${cause} (§5a, E9)`
    );
    this.name = "UnknownTagError";
    this.key = key;
    this.tags = tags;
  }
}

/** A typed HTTP error carrying the offending status and response. */
export class HttpError extends Error {
  readonly status: number;
  readonly response: Response;

  constructor(response: Response) {
    super(`HTTP ${response.status}`);
    this.name = "HttpError";
    this.status = response.status;
    this.response = response;
  }
}

/**
 * Thrown at the `getOrSet`/`getOrSetEntry` write boundary when a caller-supplied
 * tag is malformed — in particular a user tag beginning with the reserved `__`
 * prefix. A real runtime error, never `NotImplementedError`: tag
 * validation is not deferred kernel logic.
 */
export class InvalidTagError extends Error {
  readonly tag: string;

  constructor(tag: string, reason: string) {
    super(`invalid tag ${JSON.stringify(tag)}: ${reason}`);
    this.name = "InvalidTagError";
    this.tag = tag;
  }
}

/**
 * Thrown by `duration()` when a computed duration is malformed — a non-positive or
 * non-finite `value`, or (critical case) a `value × unitFactor` product that
 * is not a finite, positive, safe integer once multiplied out. A real
 * runtime error, never `NotImplementedError`: duration validation is not deferred kernel
 * logic. Mirrors `InvalidTagError` — the builder rejects a bad input, it never coerces one.
 */
export class InvalidDurationError extends Error {
  readonly value: number;
  readonly unit: string;

  constructor(value: number, unit: string, reason: string) {
    super(
      `invalid duration ${JSON.stringify(String(value) + unit)}: ${reason}`
    );
    this.name = "InvalidDurationError";
    this.value = value;
    this.unit = unit;
  }
}

/**
 * Thrown by the plain `getOrSet` form when a fill is fenced past its write-back
 * retry attempt budget: a hard delete keeps landing mid-fill/mid-
 * refresh so the accepted result never postdates every purge visible at
 * write-back, and the fill fails within its attempt budget rather than ever
 * storing or serving a fenced value. A real runtime outcome, never
 * `NotImplementedError` — the entry form (`getOrSetEntry`) surfaces the same
 * outcome as `{ value: undefined }` instead of throwing.
 */
export class FencedError extends Error {
  readonly key: string;

  constructor(key: string, message?: string) {
    super(
      message ??
        `fill for key ${JSON.stringify(key)} fenced past its write-back attempt budget (§5.0/MI6)`
    );
    this.name = "FencedError";
    this.key = key;
  }
}

/**
 * Thrown when a factory declares a dependency after it has settled: a
 * `ctx.dependsOn`/`ctx.setTags` call reaching the context AFTER the factory promise
 * resolved or rejected. The fill's tag set closes at settle, so a later declaration is
 * outside the value's lifetime and cannot join it. A real runtime error, never
 * `NotImplementedError`. Carries the offending key.
 */
export class FactorySettledError extends Error {
  readonly key: string;

  constructor(key: string) {
    super(
      `factory for key ${JSON.stringify(key)} declared a dependency after it settled (§2.1, E24)`
    );
    this.name = "FactorySettledError";
    this.key = key;
  }
}

/**
 * The `EntryFactoryCtx.skip()` sentinel (M10, invariant 24) — a private
 * throw/catch signal, analogous to how a classified factory error rides
 * `fail()`. `skip()` is typed `never` so it never falsely resolves `undefined`
 * as a caller's `T`; `fillAndStore` catches this exact class and turns it into
 * a `{ skipped: true, value: undefined }` outcome — nothing is stored, nothing
 * is served. Never part of the public surface (not exported from `index.ts`).
 */
export class SkipSignalError extends Error {
  constructor() {
    super(
      "ctx.skip() called — the fill is skipped (nothing stored, nothing served)"
    );
    this.name = "SkipSignalError";
  }
}
