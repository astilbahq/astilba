import { describe, expect, it } from "vitest";

import { measureWarm } from "./benchmark.ts";

describe("warm measurement harness", () => {
  it("records only measured iterations and calculates p95", async () => {
    const times = [0, 1, 1, 3, 3, 6, 6, 10, 10, 15];
    let operationCount = 0;
    let timeIndex = 0;

    const result = await measureWarm(
      () => {
        operationCount += 1;
      },
      {
        iterations: 5,
        now: () => {
          const time = times[timeIndex] ?? 0;
          timeIndex += 1;
          return time;
        },
        warmup: 2,
      }
    );

    expect(operationCount).toBe(7);
    expect(result).toMatchObject({
      iterations: 5,
      maximumMs: 5,
      minimumMs: 1,
      p95Ms: 5,
      warmup: 2,
    });
  });

  it.each([
    { iterations: 0, warmup: 0 },
    { iterations: -1, warmup: 0 },
    { iterations: 1.5, warmup: 0 },
    { iterations: Number.NaN, warmup: 0 },
    { iterations: Number.POSITIVE_INFINITY, warmup: 0 },
    { iterations: Number.MAX_SAFE_INTEGER + 1, warmup: 0 },
    { iterations: 1, warmup: -1 },
    { iterations: 1, warmup: 0.5 },
    { iterations: 1, warmup: Number.NaN },
    { iterations: 1, warmup: Number.POSITIVE_INFINITY },
    { iterations: 1, warmup: Number.MAX_SAFE_INTEGER + 1 },
  ])("rejects invalid sample counts: $iterations/$warmup", async (options) => {
    await expect(measureWarm(() => undefined, options)).rejects.toThrow(
      new RangeError("Invalid measurement sample counts")
    );
  });
});
