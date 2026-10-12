/* References: E29, E13. */

interface HasDurable {
  readonly durable?: boolean;
}

/** The three answers `durable` can carry, as an explicit tag — so the caller cannot accidentally
 * treat "known not durable" and "unknowable" as one. Internal: `durableVerdict`/`durableNote` are the
 * consumed surface; the tag type is theirs to share, not the module's to publish. */
type DurableVerdict = "durable" | "l1-only" | "unknowable";

/** Classify the serve-turn `durable` field into its three distinct states (E29). Explicit
 * `=== true` / `=== false` comparisons: absence (a missing property) is its own arm, never folded
 * into `false`. */
export function durableVerdict(entry: HasDurable): DurableVerdict {
  if (entry.durable === true) {
    return "durable";
  }
  if (entry.durable === false) {
    return "l1-only";
  }
  return "unknowable";
}

/** The cell text for each verdict. Kept beside the classifier so the three strings and the three
 * arms are read together. */
const DURABLE_NOTE: Readonly<Record<DurableVerdict, string>> = {
  durable:
    "yes — the value was found in L2, and the read that found it is the proof (E29)",
  "l1-only":
    "NO — the value landed in L1 only; the durable write was suppressed rather than failing the caller (E13)",
  unknowable:
    "unknowable at serve time — an L1 hit carries no write-back record, and the serve does not consult L2 to find one (§5.1/§8, E29)",
};

/** How the console renders `CacheEntry.durable` — three-state, never a truthiness collapse. */
export function durableNote(entry: HasDurable): string {
  return DURABLE_NOTE[durableVerdict(entry)];
}
