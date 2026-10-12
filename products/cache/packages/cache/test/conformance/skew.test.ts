import { SimClock, SkewedClock, world } from "@astilba/harness";
import { describe, expect, it } from "vitest";

import {
  makeFakeLockHarness,
  makeFakeRegistryHarness,
  makeFakeStoreHarness,
  referenceCodec,
} from "./fixtures/reference";
import { runLockContract } from "./runner/lock";
import { runRegistryContract } from "./runner/registry";
import { runStoreContract } from "./runner/store";

/* References: E3. */

const SKEW_MS = 86_400_000;

// Drive a fixed sequence of shared registry/lock arbitration under a given per-isolate
// wall-clock skew, collecting the observable outcomes (epochs, fences).
async function collectArbitration(
  skewA: number,
  skewB: number
): Promise<{ epochs: number[]; fences: number[] }> {
  const w = world({ isolates: 2 });
  // The actual SkewedClock skew facility, via the chaos surface.
  w.chaos.skewClock(0, skewA);
  w.chaos.skewClock(1, skewB);
  const soft = await w.registry.expire(["a"]);
  const hard = await w.registry.delete(["a"]);
  const soft2 = await w.registry.expire(["b"]);
  const lease1 = await w.lock.acquire("k");
  const lease2 = await w.lock.acquire("k");
  return {
    epochs: [soft.epoch, hard.epoch, soft2.epoch],
    fences: [lease1.fence, lease2.fence],
  };
}

// (1) Re-run a representative subset of the contracts on a hugely-skewed timeline.
// Identical green (every `it` passes) proves the Store TTL/visibility, Registry epoch
// issuance, and Lock fencing outcomes are wall-clock-invariant.
describe("SkewedClock pass — contracts hold identically on a skewed timeline", () => {
  runStoreContract(makeFakeStoreHarness(SKEW_MS), {
    codec: referenceCodec,
    label: "Store @ +1d skew",
  });
  runRegistryContract(makeFakeRegistryHarness(SKEW_MS), {
    label: "Registry @ +1d skew",
  });
  // FakeLock consults no clock at all — fences are pure counters, so the fencing
  // contract is skew-invariant by construction (the strongest form of the E3 claim).
  runLockContract(makeFakeLockHarness(), { label: "Lock (clock-free)" });
});

describe("SkewedClock pass — skew changes no arbitration outcome", () => {
  it("SkewedClock genuinely diverges from the base timeline (facility sanity)", () => {
    const base = new SimClock(0);
    const ahead = new SkewedClock(base, SKEW_MS);
    const behind = new SkewedClock(base, -SKEW_MS);
    expect(ahead.now() - behind.now()).toBe(2 * SKEW_MS);
  });

  it("shared registry/lock arbitration is identical under per-isolate wall-clock skew (E3)", async () => {
    const base = await collectArbitration(0, 0);
    const skewed = await collectArbitration(SKEW_MS, 2 * SKEW_MS);

    expect(skewed).toStrictEqual(base);
  });
});
