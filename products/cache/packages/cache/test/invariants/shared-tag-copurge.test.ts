// INV-1 — spec §11.1 (§4.1 shared-tag fan-out facet)
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

/* References: §4.1, §5.0. */

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
