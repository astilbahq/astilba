// The entry envelope with its `fence?` and `codecId` fields.

/** `val` = a cached value; `neg` = a remembered 404 (hard-expire only). */
export type EnvelopeKind = "val" | "neg";

/** Wire container of the stored bytes. `codecId` is the codec identity. */
export type EnvelopeEnc = "json" | "json+gz" | "bin";

/**
 * The stored entry (schema version 3). `bornEpoch` is the invalidation clock;
 * `bornMs` is TTL/grace/GC/age arithmetic ONLY and is never an arbiter.
 *`tags` are sorted, deduped, and include `__k:<h>` and `__ns:<name>`.
 */
export interface Envelope<T = unknown> {
  readonly v: 3;
  /** `<ns-version>:<ns>:<scope>:<user-key>`. */
  readonly key: string;
  readonly val: T;
  /** Writer's applied registry epoch at FILL START — the invalidation clock. */
  readonly bornEpoch: number;
  /** Wall time at fill start — TTL/grace/GC/age arithmetic only; never an arbiter. */
  readonly bornMs: number;
  /** Observability only. */
  readonly storedAt: number;
  readonly ttl: number;
  readonly grace: number;
  /** Sorted, deduped; includes `__k:<h>` and `__ns:<name>`. */
  readonly tags: readonly string[];
  /** `'pub' | 'usr:<h>' | 'ten:<h>'`. */
  readonly scope: string;
  readonly src: string;
  readonly kind: EnvelopeKind;
  readonly enc: EnvelopeEnc;
  readonly size: number;
  /** Codec name + wire version; decode fails closed on mismatch. */
  readonly codecId: string;
  /** Lock fencing token, carried by locked fills; an arbiter above `bornMs`. */
  readonly fence?: number;
}
