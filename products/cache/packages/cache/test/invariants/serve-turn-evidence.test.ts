// INV-4 — spec
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// INV-4's claim — a `usr:` value never reaches a shared tier — is a property of the ENTRY, not of
// the turn that happens to observe it. The suite already pins it on the FILL turn, where the fill
// did the write-back itself and reports what it did. These pin it on a SERVE turn, where the
// kernel did no writing and must reason from the identity the entry was stored under.
//
// Why exact values here (`toBe(false)`), where the fill-turn pins use `toBeFalsy`: here a
// serve reports `durable` as `true` / `false` / ABSENT, and `toBeFalsy` cannot tell known-not-
// durable (`false`) from unknowable (absent) — both are falsy. The positive half of INV-4's claim
// is that the kernel KNOWS a `usr:` entry is not durable, and a truthiness assertion cannot
// witness knowing rather than merely not-claiming. (The two `prefer-to-be-*` lint rules autofix
// this exact form away; they are scoped off for this file in vite.config.ts, with the reason.)
//
// The `pub` case is what makes the `usr:` case mean something: both are L1 hits, and they differ
// only in the stored identity. A kernel reporting `false` for every L1 hit would satisfy the
// `usr:` pin while knowing nothing at all — it is caught here.

import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import { describe, expect } from "vitest";

const privateFactory = async (): Promise<string> =>
  await Promise.resolve("private-value");
const publicFactory = async (): Promise<string> =>
  await Promise.resolve("shared-value");

describe("serve-turn evidence (§5.1, E29)", () => {
  test("INV-4: a usr:-scoped entry SERVED from L1 reports durable === false — the kernel knows it is not durable", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    const read = async (): Promise<
      Awaited<ReturnType<typeof cache.getOrSetEntry<string>>>
    > =>
      await cache.getOrSetEntry({
        factory: privateFactory,
        key: "profile",
        request: { userId: "u1" },
        tags: [tag],
      });

    // The FILL turn — L1-only by scope resolution.
    const filled = await read();
    expect(filled.durable).toBe(false);

    // THE TOOTH — a SERVE of that same entry, an L1 hit. This turn performed no write-back and
    // read no shared tier, yet the stored `usr:` identity still settles the question: an entry
    // stored under a principal scope is never published to L2, so `false` is a reading of the
    // entry, not a guess about the turn.
    const served = await read();
    expect(served.value).toBe("private-value");
    // Non-vacuity: this really is the L1-hit serve path, not a second fill wearing its clothes.
    expect(served.tier).toBe("l1");
    expect(served.durable).toBe(false);
  });

  test("INV-4 (discriminator): a pub entry served from L1 reports durable ABSENT — the false above is read off the identity, not off the tier", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    const read = async (): Promise<
      Awaited<ReturnType<typeof cache.getOrSetEntry<string>>>
    > =>
      await cache.getOrSetEntry({
        factory: publicFactory,
        key: "page",
        scope: "public",
        tags: [tag],
      });

    await read();
    // The SAME serve path as the case above — an L1 hit — differing ONLY in the stored scope. A
    // `pub` entry's L1 envelope carries no record of what its write-back did, and the read path does not
    // read L2 on an L1 hit, so this serve genuinely cannot decide. It says so by absence rather
    // than guessing: `true` would claim a shared copy it never saw, `false` would deny one that
    // is very likely there.
    const served = await read();
    expect(served.tier).toBe("l1");
    // STRUCTURAL absence — the property is not present. `toBeUndefined` alone would also accept an
    // explicitly-assigned `undefined`, which is a different thing on the wire and under the
    // compiler's exact-optional semantics.
    expect(Object.hasOwn(served, "durable")).toBe(false);
    expect(served.durable).toBeUndefined();
  });
});
