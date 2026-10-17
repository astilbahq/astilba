// The replication-mirror keyspace — the SINGLE source of truth for the
// `state`/`delta`/`snap` mirror keys AND the `__state` pointer wire type. Both hosts of
// the one machine WRITE these keys through `executeFlushEffects`, and the per-isolate
// reader (src/invalidation.ts) READS them; defining them ONCE here is what keeps the
// writer and reader from re-drifting to different bytes (the round-trip the redo
// exists to protect).
//
// SCOPING: every mirror key is registry-scoped — `__reg:<regId>:state`,
// `__reg:<regId>:delta:<seq>`, `__reg:<regId>:snap:<seq>` — so multiple Coordinators
// sharing one KV binding never overwrite each other and a reader can tell whose pointer it
// blessed. `regId` is validated to `[a-z0-9._-]`, length ≤ 64: the delimiter `:` is OUTSIDE
// that charset, so `__reg:<regId>:…` parses unambiguously — injective by construction, no
// escaping. The pointer payload ECHOES `regId`; the reader asserts the echo matches its own
// configured value (belt against any future scheme/prefix divergence).
//
// PURE: this path is inside the grep-guarded `src/**` (outside adapters); no ambient
// time/randomness — it only formats strings.

/** The default `regId` — single-registry deployments stay zero-config. */
export const DEFAULT_REG_ID = "default";

/** The `regId` charset + length bound (`[a-z0-9._-]`, ≤ 64). */
const REG_ID_PATTERN = /^[a-z0-9._-]{1,64}$/u;

/**
 * Does `regId` satisfy the charset/length (`[a-z0-9._-]`, 1–64 chars)? The pure predicate
 * behind `assertRegId` — the SINGLE source of the grammar, so the machine's throwing gate and the
 * adapter's name-derivation refusal (`resolveRegId`) test the same rule and can never drift.
 */
export function isRegId(regId: string): boolean {
  return REG_ID_PATTERN.test(regId);
}

/**
 * Validate a `regId` against the charset/length (`[a-z0-9._-]`, 1–64 chars) — the gate
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
 * The `__state` pointer contents mirrored into durable storage. Carries the
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

/** The registry-scoped mirror keys for one `regId`. */
export interface MirrorKeys {
  /** The one hot pointer key, `__reg:<regId>:state` (blessed ≤ once per flush). */
  readonly stateKey: string;
  /** The immutable delta-batch key, `__reg:<regId>:delta:<seq>`. */
  deltaKey(seq: number): string;
  /** The immutable snapshot key, `__reg:<regId>:snap:<seq>`. */
  snapKey(seq: number): string;
}

/**
 * Build the registry-scoped mirror keys for `regId`. `regId` is expected to be
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
