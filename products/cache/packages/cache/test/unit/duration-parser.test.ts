import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { world } from "@astilba/harness";
import { describe, expect, it, vi } from "vitest";

// oxlint-disable-next-line typescript/strict-void-return -- Node's custom promisification supports execFile's value-returning overload.
const execFileAsync = promisify(execFile);

async function parsedRetention(input: string): Promise<number | undefined> {
  const rig = world({
    isolates: 1,
    defaults: {
      // @ts-expect-error Exercise the runtime boundary, including malformed strings.
      maxEntryRetention: input,
    },
  });
  const retention = vi.spyOn(rig.registry, "registerRetention");
  await rig.isolate(0).cache.getOrSet({
    factory: async () => "value",
    key: "duration-parser",
    ttl: "1s",
  });
  expect(retention).toHaveBeenCalledOnce();
  return retention.mock.calls[0]?.[0];
}

describe("coarse duration parser compatibility", () => {
  it.each([
    ["250ms", 250],
    ["5s", 5000],
    ["2m", 120_000],
    ["1.5h", 5_400_000],
    ["2d", 172_800_000],
    [".5s", 500],
    ["0.5s", 500],
    ["001s", 1000],
    ["00.125s", 125],
    ["0ms", 0],
    [".0ms", 0],
    ["0.1ms", 0.1],
  ])("preserves %s as %s milliseconds", async (input, expected) => {
    await expect(parsedRetention(input)).resolves.toBe(expected);
  });

  it.each([
    "",
    "ms",
    ".ms",
    "1.ms",
    "-1s",
    "+1s",
    "1e3s",
    "1S",
    "1w",
    " 1s",
    "1s ",
    "1..2s",
    "1",
  ])("preserves the zero fallback for malformed %s", async (input) => {
    await expect(parsedRetention(input)).resolves.toBe(0);
  });

  it("handles long malformed durations without stalling any parser call site", async () => {
    const fixture = fileURLToPath(
      new URL("fixtures/duration-input.ts", import.meta.url)
    );
    // A separate process makes the deadline enforceable even if a synchronous
    // regex regression blocks its event loop. This is not a microbenchmark.
    const { stdout } = await execFileAsync(
      process.execPath,
      ["--import", "tsx", fixture],
      { killSignal: "SIGKILL", maxBuffer: 1024, timeout: 10_000 }
    );
    expect(stdout).toBe("[0,0,1000]");
  }, 20_000);
});
