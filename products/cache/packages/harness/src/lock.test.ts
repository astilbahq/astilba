import { describe, expect, test } from "vitest";

import { FakeLock } from "./lock";

/* References: INV-17. */

describe(FakeLock, () => {
  test("issues strictly monotonic fencing tokens", async () => {
    const lock = new FakeLock();
    const first = await lock.acquire("k");
    const second = await lock.acquire("k");
    expect(first.fence).toBe(1);
    expect(second.fence).toBe(2);
    expect(second.fence).toBeGreaterThan(first.fence);
    expect(lock.lastFence).toBe(2);
  });

  test("tracks held keys across acquire/release", async () => {
    const lock = new FakeLock();
    const handle = await lock.acquire("k");
    expect(lock.isHeld("k")).toBeTruthy();
    await handle.release();
    expect(lock.isHeld("k")).toBeFalsy();
  });
});
