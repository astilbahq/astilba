// Ordinary unit pin (NOT sacred) — CACHE-28-ii.
//
// Guards the MEANING of the co-tag fan-out floor (`shared-tag-copurge`) and, transitively, the
// distributions the Stage-2 chaos floors are calibrated against: the DEFAULT `commandsArb`
// generator uses `NARROW_LAYOUT` (key `k<i>` ↔ its own tag `t<i>`, 1:1), so a hard delete can never
// reach a co-tagged SIBLING — there is none. If `NARROW_LAYOUT` ever silently gained a shared tag,
// the default generator would start producing fan-out, and the four floor properties' calibrated
// distributions would shift underneath them with no failing test to say so. This pins the purity
// directly: run the default generator and assert it laid ZERO co-tag fan-out.

import {
  coTagFanCases,
  commandsArb,
  modelSetup,
  resetCoTagFanCases,
} from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { afterAll, beforeAll, describe, expect } from "vitest";

describe("NARROW-layout purity", () => {
  beforeAll(() => {
    resetCoTagFanCases();
  });

  test.prop([commandsArb()])(
    "the default (NARROW) generator never lays a shared tag",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );

  afterAll(() => {
    // 1:1 layout ⇒ a tag indexes only its own key ⇒ a delete never clears a sibling ⇒ zero fan-out.
    // A single fan case here would prove `NARROW_LAYOUT` grew a shared tag (regression).
    expect(coTagFanCases()).toBe(0);
  });
});
