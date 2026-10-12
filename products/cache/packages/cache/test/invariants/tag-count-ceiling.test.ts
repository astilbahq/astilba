// MI13 — spec §2.3, errata E24 (the ≤126 user-tag count ceiling)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { InvalidTagError } from "@astilba/cache";
import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import { describe, expect } from "vitest";

async function rejectionOf(promise: Promise<unknown>): Promise<unknown> {
  return await promise.then(
    () => null,
    (error: unknown) => error
  );
}

/** N distinct user tags. */
function nTags(n: number): ReturnType<typeof mkTag>[] {
  return Array.from({ length: n }, (_unused, i) => mkTag(`t:${i}`));
}

describe("tag count ceiling (§2.3, MI13)", () => {
  test("MI13/E24: at most 126 user tags — 126 passes, 127 is rejected at write (InvalidTagError)", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);

    // 126 user tags is the ceiling — accepted.
    const ok = await cache.getOrSet({
      factory: async () => await Promise.resolve("v"),
      key: "ok",
      tags: nTags(126),
    });
    expect(ok).toBe("v");

    // 127 exceeds it — rejected at the write boundary, and specifically as InvalidTagError (the
    // typed refusal, not a crash and not NotImplementedError): those entries would have had an
    // undefined purge topology, so the refusal is the repair.
    const err = await rejectionOf(
      cache.getOrSet({
        factory: async () => await Promise.resolve("v"),
        key: "over",
        tags: nTags(127),
      })
    );
    expect(err).toBeInstanceOf(InvalidTagError);
  });
});
