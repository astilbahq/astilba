// The consistency contract and the registry's dual-watermark epoch model
// with tri-state tag knowledge. No wall-clock term appears in validation —
// epochs are minted by a single authority.

/** Public consistency levels. `local` was deleted from the public union (M6). */
export type Consistency = "strong" | "eventual";

/**
 * Dual watermark epochs for a tag — independent, monotone, max-on-write. A
 * later soft purge can never regress a hard purge (invariant 14).
 */
export interface Watermark {
  readonly softEpoch: number;
  readonly hardEpoch: number;
}

/**
 * Tri-state tag knowledge. Either `known` (with epoch provenance) or
 * absent = `unknown`. An absent record is `unknown`, never `{0,0}`. `unknown`
 * fails closed: never fresh, never grace (invariant 11) — a gap of unknown
 * hardness may hide a hard purge.
 */
export type TagKnowledge =
  | {
      readonly known: true;
      readonly softEpoch: number;
      readonly hardEpoch: number;
      readonly throughEpoch: number;
    }
  | { readonly known: false };

/** The outcome of validating an entry against tag knowledge (table). */
export type Validity = "fresh" | "stale" | "dead" | "unknown";
