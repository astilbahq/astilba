// The consistency contract (§1) and the registry's dual-watermark epoch model
// with tri-state tag knowledge (§4.1). No wall-clock term appears in validation —
// epochs are minted by a single authority (§1(3), §4.1).

/** Public consistency levels (§1). `local` was deleted from the public union (M6). */
export type Consistency = "strong" | "eventual";

/**
 * Dual watermark epochs for a tag — independent, monotone, max-on-write (§4.1). A
 * later soft purge can never regress a hard purge (invariant 14).
 */
export interface Watermark {
  readonly softEpoch: number;
  readonly hardEpoch: number;
}

/**
 * Tri-state tag knowledge (§4.1, B3). Either `known` (with epoch provenance) or
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

/** The outcome of validating an entry against tag knowledge (§4.1 table). */
export type Validity = "fresh" | "stale" | "dead" | "unknown";
