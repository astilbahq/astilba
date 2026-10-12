// The entry envelope — §3, with errata E3 (`fence?`) and E8 (`codecId`).

/** `val` = a cached value; `neg` = a remembered 404 (hard-expire only) — §3, N1/N2. */
export type EnvelopeKind = "val" | "neg";

/** Wire container of the stored bytes (§3). `codecId` is the codec identity (E8). */
export type EnvelopeEnc = "json" | "json+gz" | "bin";

/**
 * The stored entry (schema version 3). `bornEpoch` is the invalidation clock;
 * `bornMs` is TTL/grace/GC/age arithmetic ONLY and is never an arbiter (§1(3),
 * E3). `tags` are sorted, deduped, and include `__k:<h>` and `__ns:<name>`.
 */
export interface Envelope<T = unknown> {
  readonly v: 3;
  /** `<ns-version>:<ns>:<scope>:<user-key>`. */
  readonly key: string;
  readonly val: T;
  /** Writer's applied registry epoch at FILL START — the invalidation clock (§3). */
  readonly bornEpoch: number;
  /** Wall time at fill start — TTL/grace/GC/age arithmetic only; never an arbiter (§3, E3). */
  readonly bornMs: number;
  /** Observability only (§3). */
  readonly storedAt: number;
  readonly ttl: number;
  readonly grace: number;
  /** Sorted, deduped; includes `__k:<h>` and `__ns:<name>` (§3). */
  readonly tags: readonly string[];
  /** `'pub' | 'usr:<h>' | 'ten:<h>'` (§3). */
  readonly scope: string;
  readonly src: string;
  readonly kind: EnvelopeKind;
  readonly enc: EnvelopeEnc;
  readonly size: number;
  /** Codec name + wire version; decode fails closed on mismatch (E8). */
  readonly codecId: string;
  /** Lock fencing token, carried by locked fills; an arbiter above `bornMs` (E3). */
  readonly fence?: number;
}
