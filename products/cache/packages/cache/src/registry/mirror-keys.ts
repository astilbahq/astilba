/* References: §4.2, E19. */

export const DEFAULT_REG_ID = "default";

/** The E19 `regId` charset + length bound (`[a-z0-9._-]`, ≤ 64). */
const REG_ID_PATTERN = /^[a-z0-9._-]{1,64}$/u;

/**
 * Does `regId` satisfy the E19 charset/length (`[a-z0-9._-]`, 1–64 chars)? The pure predicate
 * behind `assertRegId` — the SINGLE source of the grammar, so the machine's throwing gate and the
 * adapter's name-derivation refusal (E19-b, `resolveRegId`) test the same rule and can never drift.
 */
export function isRegId(regId: string): boolean {
  return REG_ID_PATTERN.test(regId);
}

/**
 * Validate a `regId` against the E19 charset/length (`[a-z0-9._-]`, 1–64 chars) — the gate
 * that makes the scoped keyspace injective without escaping (the delimiter `:` cannot occur
 * inside a `regId`). Called at `configure` time (the machine's single seeding path), so a
 * malformed id fails LOUD before it can ever mint an ambiguous key. Throws on violation.
 */
export function assertRegId(regId: string): void {
  if (!isRegId(regId)) {
    throw new Error(
      `invalid regId ${JSON.stringify(regId)} — must match [a-z0-9._-], length 1-64 (E19)`
    );
  }
}

/**
 * The `__state` pointer contents mirrored into durable storage (§4.2, E19). Carries the
 * durable cursors a reader resyncs against PLUS `regId`, which the reader asserts equals its
 * own configured value — a mismatch means it read a foreign (or unscoped) keyspace and it
 * fail-closes. One serialization per host: the machine builds this literal in alphabetical
 * key order, so the content-addressed bytes match across hosts.
 */
export interface StatePointer {
  readonly epoch: number;
  readonly flushedAt: number;
  readonly lastBatch: number;
  readonly regId: string;
  readonly snapBatch: number;
}

/** The registry-scoped mirror keys for one `regId` (§4.2, E19). */
export interface MirrorKeys {
  /** The one hot pointer key, `__reg:<regId>:state` (blessed ≤ once per flush). */
  readonly stateKey: string;
  /** The immutable delta-batch key, `__reg:<regId>:delta:<seq>`. */
  deltaKey(seq: number): string;
  /** The immutable snapshot key, `__reg:<regId>:snap:<seq>`. */
  snapKey(seq: number): string;
}

/**
 * Build the registry-scoped mirror keys for `regId` (§4.2, E19). `regId` is expected to be
 * already validated (it enters the machine only via `configure`, which calls `assertRegId`);
 * this is a pure formatter on the hot flush/resync path, so it does not re-validate. Both the
 * writer (execute.ts) and the reader (invalidation.ts) address keys ONLY through here.
 */
export function mirrorKeys(regId: string): MirrorKeys {
  const prefix = `__reg:${regId}:`;
  return {
    deltaKey: (seq: number): string => `${prefix}delta:${seq}`,
    snapKey: (seq: number): string => `${prefix}snap:${seq}`,
    stateKey: `${prefix}state`,
  };
}
