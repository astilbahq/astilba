import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mock, test } from "node:test";
import { withFetchAttempts } from "./image-fetch.mjs";

mock.method(console, "warn", () => {});

const failing = (count, value = "built") => {
  const limits = [];
  const step = (limit) => {
    limits.push(limit);
    if (limits.length <= count) throw new Error(`failure ${limits.length}`);
    return value;
  };
  return { limits, step };
};
const clock = () => {
  let elapsed = 0;
  return {
    now: () => elapsed,
    sleep: async (ms) => {
      elapsed += ms;
    },
  };
};

test("one dropped connection does not fail the step", async () => {
  const { limits, step } = failing(1);
  assert.equal(await withFetchAttempts("Step", 600_000, step, clock()), "built");
  assert.equal(limits.length, 2);
});

test("a step that keeps failing stops after three attempts", async () => {
  const { limits, step } = failing(5);
  await assert.rejects(
    withFetchAttempts("Step", 600_000, step, clock()),
    /failure 3/,
  );
  assert.equal(limits.length, 3);
});

test("the attempts share the time limit rather than each having their own", async () => {
  const { limits, step } = failing(2);
  await withFetchAttempts("Step", 600_000, step, clock());
  assert.deepEqual(limits, [600_000, 580_000, 540_000]);
});

test("no attempt starts once the limit would be spent", async () => {
  const { limits, step } = failing(5);
  await assert.rejects(
    withFetchAttempts("Step", 15_000, step, clock()),
    /failure 1/,
  );
  assert.equal(limits.length, 1);
});

test("a command that used up the limit is not started again", async () => {
  let attempts = 0;
  await assert.rejects(
    withFetchAttempts(
      "Step",
      60_000,
      () => {
        attempts++;
        execFileSync("sleep", ["5"], { timeout: 50 });
      },
      clock(),
    ),
    { code: "ETIMEDOUT" },
  );
  assert.equal(attempts, 1);
});
