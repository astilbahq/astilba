// INV-1 (shared-tag fan-out facet)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import {
  coTagFanCases,
  commandsArbWide,
  mkKey,
  mkTag,
  modelSetup,
  resetCoTagFanCases,
  world,
} from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { afterAll, beforeAll, describe, expect } from "vitest";

// The SHARED-TAG facet of invariant 1: a hard watermark is per-TAG, so a hard
// `delete` on a tag makes EVERY co-tagged key's pre-delete value illegal to serve — not
// only the one key a 1:1 tag happens to name. The default `commandsArb` generator pairs
// each key with its own tag (1:1), so it structurally CANNOT exercise this: a co-tagged
// sibling never exists there. `commandsArbWide` lays the SAME command classes over
// `WIDE_LAYOUT` (one tag over several keys), and the model's tag→keys index fans a hard
// delete's legal-set clear over every co-tagged key — so the same `observed ⊆ legal`
// oracle now bites when the kernel serves a SIBLING's value born before the shared purge.
//
// Defense-in-depth — which mutation reds which layer:
//   • Blinding the kernel's hard-purge classification (`classify`'s per-tag `dead` return)
//     reds THIS property AND the 1:1 `purge-visibility` property. That shared red is
//     evidence of ONE shared per-tag mechanism, not a missing tooth: single-key purge and
//     co-tag fan-out are the same `classify` branch, so no mutation isolates the sibling
//     case while leaving single-key visibility green.
//   • This property's DISTINCT layer is CONFIGURATION reach — N keys under one tag, which
//     the 1:1 default generator structurally cannot produce. It is the standing guard for
//     the day the mechanism SPLITS: a future per-key shortcut that kept single-key purge
//     working but skipped co-tagged siblings would red HERE while `purge-visibility` stayed
//     green. The tooth exists before the branch does.
//
// Fence sentinel — do NOT add a `FencedError` catch to this suite. A caller-visible
// `FencedError` is unreachable in this sequential command lane: every path that writes a
// tag's hard high-water advances the applied high-water in the SAME step, so the
// fence-taught retry re-mints `bornEpoch` from that co-advanced floor and
// rescues within one attempt; exhaustion needs a 3× mid-factory applied-delete race only
// `fc.scheduler` produces, which `fc.commands` cannot. A fence surfacing here is therefore
// a real finding — let it propagate loudly and ADJUDICATE it, never discard it. Tolerating
// the impossible would blindfold this suite for the day it becomes possible.

// R3 fan-reached floor: how many cases (property runs) must lay a shared-tag configuration
// whose hard `delete` fans over a co-tagged SIBLING — the substrate the oracle needs to bite.
// It counts CONFIGURATION reach (a fan clear touched a sibling), not oracle assertions. At the
// pinned config (seed 0x5eed, numRuns 100) the wide generator reaches fan-out in 7 runs; other
// seeds at 100 runs give 11-17, and it scales with numRuns (~266 at 2000). The floor sits below
// all of those so higher numRuns (stress) or seed jitter cannot flake it; a generator skewed to
// starve shared-tag layouts (its liar) drops the count to 0 and reds.
//
// This is a COVERAGE floor tied to the pinned run budget, not a correctness assertion, and it is
// deliberately coupled to numRuns=100. fast-check clusters longer (fan-out-reaching) sequences in
// later runs, so DRASTICALLY lowering numRuns can legitimately red this floor — the floor
// reporting that coverage fell below the calibrated budget, working as intended. Restore the run
// budget rather than lowering this floor (a sacred floor is warrant-guarded). The count is also
// fast-check-version-sensitive (the ^4.8.0 caret, rule 7) — re-verify it on any fast-check bump.
const FAN_REACHED_FLOOR = 3;

describe("shared-tag co-purge", () => {
  beforeAll(() => {
    resetCoTagFanCases();
  });

  test("INV-1: a hard delete on a shared tag kills a co-tagged SIBLING's pre-delete value", async () => {
    // Two DISTINCT keys carry ONE shared tag; the delete targets the tag, and a strong
    // read of the SIBLING (never itself named by the delete) must not serve its dead value.
    const w = world({ isolates: 2 });
    const { cache } = w.isolate(0);
    const tag = mkTag("shared");
    const kA = mkKey("kA");
    const kB = mkKey("kB");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("a1"),
      key: kA,
      tags: [tag],
    });
    await cache.getOrSet({
      factory: async () => await Promise.resolve("b1"),
      key: kB,
      tags: [tag],
    });
    // Hard-purge the shared tag. Both kA and kB were born before it ⇒ both are now dead.
    await cache.delete({ tag });
    // The SIBLING kB — not the key the app most recently touched, and carrying a value the
    // purge never named directly — must refill, never serve the hard-deleted "b1".
    const servedB = await cache.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("b2"),
      key: kB,
      tags: [tag],
    });
    expect(servedB).toBe("b2");
    expect(servedB).not.toBe("b1");
    // And the other co-tagged key kA is equally dead — the fan-out reaches every sibling.
    const servedA = await cache.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("a2"),
      key: kA,
      tags: [tag],
    });
    expect(servedA.value).toBe("a2");
    expect(servedA.stale).toBeFalsy();
  });

  test.prop([commandsArbWide()])(
    "INV-1 property: under a shared tag, observed serve ⊆ legal serves for EVERY co-tagged key",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );

  afterAll(() => {
    // The property is vacuous if the generator never lays a shared-tag layout whose delete
    // reaches a sibling; the floor makes that regression a red test rather than a silent pass.
    expect(coTagFanCases()).toBeGreaterThanOrEqual(FAN_REACHED_FLOOR);
  });
});
