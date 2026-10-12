import { InvalidDurationError } from "./errors";

// Duration grammar — §2.1, MI28, errata F31.
//   - template-literal type `${number}${unit}`; fractions allowed
//   - `m` is minutes; there is no month unit
//   - `<=0` / non-finite / unit-less reject at parse
//   - computed milliseconds must be a finite, positive, safe integer (F31),
//     asserted post-multiplication

/** Duration units. `m` = minutes (no month unit) — MI28. */
export type DurationUnit = "ms" | "s" | "m" | "h" | "d";

/** A duration literal, e.g. `'5m'`, `'1.5h'`, `'250ms'` (§2.1). */
export type Duration = `${number}${DurationUnit}`;

/**
 * Milliseconds per unit — the ONE `Duration`→ms grammar source (§2.1, MI28). Shared with
 * `cache.ts`'s coarse `durationMs` parser (which imports this const) so the F31 overflow
 * check here and the retention/heartbeat parse there agree on the factor table byte-for-byte.
 * `m` = minutes.
 */
export const DURATION_UNIT_MS: Record<DurationUnit, number> = {
  d: 86_400_000,
  h: 3_600_000,
  m: 60_000,
  ms: 1,
  s: 1000,
};

/**
 * Build a `Duration` from computed values (§2.1) — the trust point for
 * programmatically-derived durations (as opposed to a `'5m'` string literal the type
 * system already constrains). Rejects a non-positive or non-finite `value`, and — F31,
 * the case a naive impl passes — a `value × unitFactor` product that is not a finite,
 * positive, SAFE integer once multiplied out. So `duration(Number.MAX_SAFE_INTEGER, 'd')`
 * is REJECTED even though the input is a safe integer: the overflow lives in the product.
 * Throws `InvalidDurationError` on any violation; never coerces.
 */
export function duration(value: number, unit: DurationUnit): Duration {
  if (!Number.isFinite(value) || value <= 0) {
    throw new InvalidDurationError(
      value,
      unit,
      "value must be a finite number greater than 0 (§2.1)"
    );
  }
  // F31: the guard is POST-MULTIPLICATION. `Number.isSafeInteger` is false for NaN/±Infinity
  // and for any magnitude beyond 2^53−1, so an overflowing product (e.g.
  // MAX_SAFE_INTEGER × 86_400_000) is caught here even though `value` alone is a safe integer.
  const ms = value * DURATION_UNIT_MS[unit];
  if (!Number.isSafeInteger(ms) || ms <= 0) {
    throw new InvalidDurationError(
      value,
      unit,
      "computed milliseconds must be a finite, positive, safe integer (F31)"
    );
  }
  return `${value}${unit}`;
}
