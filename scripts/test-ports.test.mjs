import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { promisify } from "node:util";
import { blockSize, holdCheckout, reservePortBlock } from "./test-ports.mjs";

const execute = promisify(execFile);
const script = new URL("test-ports.mjs", import.meta.url).pathname;

// A range of its own, so suites running beside this test keep theirs.
const range = { first: 15900, last: 15990 };

test("another run's block is skipped, and so is one with a port in use", async (t) => {
  const first = await reservePortBlock(range);
  // A second process cannot take the block this one holds.
  const { stdout } = await execute(process.execPath, [
    "--input-type=module",
    "-e",
    `import { reservePortBlock } from ${JSON.stringify(script)}; console.log(await reservePortBlock(${JSON.stringify(range)}));`,
  ]);
  assert.notEqual(Number(stdout), first);
  const busy = createServer().listen(first + 2 * blockSize + 3, "127.0.0.1");
  await once(busy, "listening");
  t.after(() => busy.close());
  const next = await reservePortBlock({ ...range, first: first + 2 * blockSize });
  assert.ok(next > first + 2 * blockSize);
});

test("a checkout runs one suite at a time and takes over an exited holder's lock", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "astilba-test-ports-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const lock = join(directory, "suite.lock");
  const hold = `import { holdCheckout } from ${JSON.stringify(script)}; holdCheckout(${JSON.stringify(lock)}, "The suite"); console.log("held");`;
  // A process that exited without removing its lock, as after SIGKILL.
  const exited = (await execute(process.execPath, ["-e", "console.log(process.pid)"])).stdout.trim();
  await writeFile(lock, exited);
  holdCheckout(lock, "The suite");
  assert.equal(await readFile(lock, "utf8"), String(process.pid));
  await assert.rejects(
    execute(process.execPath, ["--input-type=module", "-e", hold]),
    /The suite is already running in this checkout \(process \d+\)/,
  );
  assert.equal(await readFile(lock, "utf8"), String(process.pid));
});
