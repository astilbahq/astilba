import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { setTimeout as pause } from "node:timers/promises";

// How the tools here report bytes that fail their pin, whichever of them checked.
// Asking again cannot help, so these stop at once. The Docker wording was measured
// against a registry serving changed bytes, on both image stores. Anything else
// repeats, because a reset can read as anything: the containerd store reports one
// on a pinned manifest as "not found", and a short layer as a short read. pnpm also
// logs "(ERR_PNPM_TARBALL_INTEGRITY). Will retry" for a tarball it then recovers.
const integrityFailure =
  /unexpected commit digest|failed size validation|verification failed for digest|checksum mismatch|SECURITY ERROR|ERR_PNPM_TARBALL_INTEGRITY(?!\))|does not match its pinned hash/;

const threeAttempts = (attempt) => [20_000, 40_000][attempt];

// A partial outage lasts minutes, so asking again for seconds does not outlast it;
// see docs/continuous-integration.md#host-downloads.
const outlastOutage = (_attempt, elapsed) =>
  elapsed < 240_000 ? 15_000 : undefined;

// A command that ran and failed, or a download that did not arrive. Only these repeat:
// anything else, such as a command that could not start or a bug in the step, would
// fail the same way again.
class FetchFailure extends Error {}

// Runs a step that fetches over the network until it succeeds or `delay` gives up.
// With a time limit, the attempts share it and each receives what remains. A step
// that used up the limit is stuck rather than dropped, so it stops, as does a failed
// integrity check. The final error names the step.
export async function withFetchAttempts(
  name,
  step,
  {
    limitMs = Infinity,
    delay = threeAttempts,
    sleep = pause,
    now = Date.now,
  } = {},
) {
  const started = now();
  const deadline = started + limitMs;
  for (let attempt = 0; ; attempt++) {
    try {
      // A timeout of 0 would mean no limit, and a sleep can overrun what remains.
      return await step(Math.max(deadline - now(), 1));
    } catch (error) {
      const wait =
        error instanceof FetchFailure &&
        error.code !== "ETIMEDOUT" &&
        !integrityFailure.test(error.stderr ?? error.message)
          ? delay(attempt, now() - started)
          : undefined;
      if (wait === undefined || now() + wait >= deadline) {
        error.message = `${name} failed: ${error.message}`;
        throw error;
      }
      console.warn(
        `::warning::${name} failed: ${error.message}. Trying again in ${wait / 1000} seconds`,
      );
      await sleep(wait);
    }
  }
}

// Runs a command with its errors in the job log as they happen, and keeps them so
// the failure can be read. Resolves with stdout when asked to capture it.
export function runLogged(
  command,
  args,
  { cwd, env, timeoutMs = Infinity, captureStdout = false } = {},
) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env,
      stdio: ["ignore", captureStdout ? "pipe" : "inherit", "pipe"],
      timeout: Number.isFinite(timeoutMs) ? timeoutMs : undefined,
    });
    let stdout = "";
    let stderr = "";
    child.stdout?.setEncoding("utf8").on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.setEncoding("utf8").on("data", (chunk) => {
      process.stderr.write(chunk);
      stderr = (stderr + chunk).slice(-65_536);
    });
    child.on("error", reject);
    child.on("close", (code, signal) => {
      if (code === 0) return resolve(stdout);
      const reason =
        stderr.trim().split("\n").at(-1) || `exited with ${code ?? signal}`;
      reject(
        Object.assign(new FetchFailure(reason), {
          stderr,
          code: child.killed ? "ETIMEDOUT" : code,
        }),
      );
    });
  });
}

// Pulls one at a time. A pinned digest makes every attempt verify the same bytes.
export async function pullImages(images, docker = ["docker"], options = {}) {
  const [command, ...args] = docker;
  for (const image of images) {
    if (!/@sha256:[0-9a-f]{64}$/.test(image))
      throw new Error(`${image} needs a digest`);
    await withFetchAttempts(
      `Pulling ${image}`,
      (timeoutMs) =>
        runLogged(command, [...args, "pull", image], { timeoutMs }),
      { delay: outlastOutage, ...options },
    );
  }
}

// Repeats a command that fetches for itself. A command that ran to completion is
// never repeated, so whatever it reported stands.
export const fetchingCommand = (name, command, args, run, attempts) =>
  withFetchAttempts(
    name,
    () => runLogged(command, args, { ...run, captureStdout: true }),
    { delay: outlastOutage, ...attempts },
  );

async function fetchBytes(url, idleMs, headers) {
  const stalled = new AbortController();
  const idle = setTimeout(
    () => stalled.abort(new Error(`no data for ${idleMs} ms`)),
    idleMs,
  );
  try {
    const response = await fetch(url, { headers, signal: stalled.signal });
    if (!response.ok) {
      await response.body?.cancel();
      throw new Error(`HTTP ${response.status}`);
    }
    const chunks = [];
    for await (const chunk of response.body) {
      idle.refresh();
      chunks.push(chunk);
    }
    return Buffer.concat(chunks);
  } catch (error) {
    // fetch reports a dropped connection only in its cause, whose message is
    // empty when every address of a host refused.
    throw new FetchFailure(
      error.cause?.message || error.cause?.code || error.message,
    );
  } finally {
    clearTimeout(idle);
  }
}

// A stall repeats and a slow download finishes.
export const download = (
  url,
  { idleMs = 60_000, headers, ...options } = {},
) =>
  withFetchAttempts(`Download from ${url}`, () => fetchBytes(url, idleMs, headers), {
    delay: outlastOutage,
    ...options,
  });

// A file that arrives and fails its hash stops at once, because a download did not
// fail.
export async function downloadVerified(url, algorithm, expected, options) {
  const bytes = await download(url, options);
  const actual = createHash(algorithm).update(bytes).digest("hex");
  if (actual !== expected)
    throw new Error(
      `Download from ${url} does not match its pinned hash: expected ${algorithm} ${expected}, got ${actual}`,
    );
  return bytes;
}
