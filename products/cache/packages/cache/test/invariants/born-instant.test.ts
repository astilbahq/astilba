// INV-31 — spec §3, errata E29 (the born instant; `age` derived from it)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// Birth is an event, and a value has one of them. The stamp is minted once, when the factory
// settles, and every copy of that value carries THAT number — the L2 write and the L1 hydration
// alike. A tier that reads a clock of its own instead is not recording when the value was born; it
// is recording when that tier finished its own write, and the same value then has two ages.
//
// The error is not random: it equals however long the durable write took. So the reading is least
// accurate exactly when someone is looking — during the slow writes of an incident — and it fails
// in the more misleading direction, since the copy that took longer to store looks younger. An
// entry must never appear to get OLDER by losing a tier, and never younger by gaining one: age
// describes the value, not the shelf it was found on.
//
// The write-cost knob is what makes the defect visible at all: with free writes the two clock
// reads coincide and a re-minted stamp is indistinguishable from a carried one.

import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import { describe, expect } from "vitest";

const L2_WRITE_COST_MS = 500;
const SETTLE_MS = 1000;

describe("the born instant (§3, E29)", () => {
  test("INV-31: one value has ONE age — the L1 copy and the L2 copy report it identically, however long the durable write took", async () => {
    const w = world({ isolates: 2, replicas: 1 });
    const tag = mkTag("t0");
    // A durable write costs real time (§8). The filling isolate mints `bornMs`, then spends this
    // long writing L2 before it hydrates its own L1 — the exact window in which a second clock
    // read would drift.
    w.chaos.setL2WriteCost(L2_WRITE_COST_MS);

    const filler = w.isolate(0).cache;
    await filler.getOrSetEntry({
      factory: async () => await Promise.resolve("v"),
      key: "k",
      tags: [tag],
    });

    // Let the value age by a known amount, so both reads below are asking a real question.
    await w.advance(SETTLE_MS);

    // The SAME value, read from the two tiers it lives in: isolate 0 hits its own hydrated L1;
    // isolate 1 has never seen the key, so it reads the shared L2 copy.
    const fromL1 = await filler.getOrSetEntry({
      factory: async () => await Promise.resolve("refilled"),
      key: "k",
      tags: [tag],
    });
    const fromL2 = await w.isolate(1).cache.getOrSetEntry({
      factory: async () => await Promise.resolve("refilled"),
      key: "k",
      tags: [tag],
    });

    // Non-vacuity: both really served the STORED value from the tier named, rather than refilling
    // (a refill would report `tier:"origin"` and age 0, and the ages would agree for the wrong
    // reason).
    expect({ l1: fromL1.tier, l2: fromL2.tier }).toStrictEqual({
      l1: "l1",
      l2: "l2",
    });
    expect({ l1: fromL1.value, l2: fromL2.value }).toStrictEqual({
      l1: "v",
      l2: "v",
    });
    // THE TOOTH — to the millisecond. Re-minting `bornMs` at hydration dates the L1 copy to the
    // moment the L2 write finished, so it reports an age short by exactly the write's cost.
    expect(fromL1.age).toBe(fromL2.age);
  });
});
