// How the console renders `CacheEntry.durable` — the serve-turn's THREE answers, kept
// apart on purpose.
//
// `durable` is a three-state serve-evidence report: a serve says what the serve can prove.
//   • `true`  — the value was found in L2; the read that found it IS the proof it is durable.
//   • `false` — the stored scope resolves l1-only (a `usr:` entry); the kernel KNOWS it is not
// durable, and that positive knowledge is the "landed only in L1".
//   • ABSENT  — a `pub`/`ten:` entry found in L1, or an unreadable scope: the write-through outcome
//               is unknowable at serve time. The L1 envelope carries no write-back record, and the
//               serve does not consult L2 on an L1 hit — so the honest report is "cannot tell".
//
// WHY A PURE FUNCTION AND NOT A TERNARY IN THE JSX. The whole risk this file exists to remove is the
// truthiness collapse: `entry.durable ? "yes" : "…L1 only…"` reads ABSENT as the confident lie
// "not durable", because a missing property reads `undefined` and `undefined` is falsy. `false` and
// absent are DIFFERENT answers here — one is "the kernel knows it is not durable", the other is
// "nobody can tell from this serve" — and a consumer that must tell them apart discriminates
// EXPLICITLY. So the discrimination lives here, in a function a tooth can feed all three inputs,
// rather than in a conditional a rendering test cannot reach.
//
// IT TAKES THE ENTRY-SHAPED OBJECT, NOT A BARE `boolean | undefined`, and that is deliberate:
// absence arrives as a MISSING PROPERTY (the exact-optional `durable?`), so modelling it as a
// missing key rather than an explicit `undefined` is the faithful shape — and it is the one the
// tooth exercises.

/** Just the field this module reads — `CacheEntry`'s `durable?`, and nothing else it does not need. */
interface HasDurable {
  readonly durable?: boolean;
}

/** The three answers `durable` can carry, as an explicit tag — so the caller cannot accidentally
 * treat "known not durable" and "unknowable" as one. Internal: `durableVerdict`/`durableNote` are the
 * consumed surface; the tag type is theirs to share, not the module's to publish. */
type DurableVerdict = "durable" | "l1-only" | "unknowable";

/** Classify the serve-turn `durable` field into its three distinct states. Explicit
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
