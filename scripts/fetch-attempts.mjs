import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { setTimeout as pause } from "node:timers/promises";

// How the tools here report bytes that fail their pin, whichever of them checked.
// Asking again cannot help, so these stop at once. The Docker wording was measured
// against a registry serving changed bytes, on both image stores. Anything else
// repeats, because a reset can read as anything: the containerd store reports one
// on a pinned manifest as "not found".
const integrityFailure =
  /unexpected commit digest|verification failed for digest|checksum mismatch|SECURITY ERROR|EINTEGRITY|ERR_PNPM_TARBALL_INTEGRITY|does not match its pinned hash/;

const threeAttempts = (attempt) => [20_000, 40_000][attempt];

// A partial outage lasts minutes, so asking again for seconds does not outlast it;
// see docs/continuous-integration.md#host-downloads.
const outlastOutage = (_attempt, elapsed) =>
  elapsed < 240_000 ? 15_000 : undefined;

// Runs a step that fetches over the network until it succeeds or `delay` gives up.
// With a time limit, the attempts share it and each receives what remains. A step
// that used up the limit is stuck rather than dropped, so it stops, as do a failed
// integrity check and a command that could not start. The final error names the step.
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
      const wait = delay(attempt, now() - started);
      if (
        wait === undefined ||
        error.code === "ETIMEDOUT" ||
        error.syscall?.startsWith("spawn") ||
        integrityFailure.test(error.stderr ?? error.message) ||
        now() + wait >= deadline
      ) {
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
        Object.assign(new Error(reason), {
          stderr,
          code: child.killed ? "ETIMEDOUT" : code,
        }),
      );
    });
  });
}

// Pulls one at a time. A pinned digest makes every attempt verify the same bytes.
export async function pullImages(
  images,
  docker = ["docker"],
  { limitMs, ...options } = {},
) {
  const [command, ...args] = docker;
  for (const image of images) {
    if (!/@sha256:[0-9a-f]{64}$/.test(image))
      throw new Error(`${image} needs a digest`);
    await withFetchAttempts(
      `Pulling ${image}`,
      (timeoutMs) =>
        runLogged(command, [...args, "pull", image], { timeoutMs }),
      { limitMs, delay: outlastOutage, ...options },
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

async function fetchBytes(url, idleMs) {
  const stalled = new AbortController();
  const idle = setTimeout(
    () => stalled.abort(new Error(`no data for ${idleMs} ms`)),
    idleMs,
  );
  try {
    const response = await fetch(url, { signal: stalled.signal });
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
    throw new Error(error.cause?.message || error.cause?.code || error.message);
  } finally {
    clearTimeout(idle);
  }
}

// A stall repeats and a slow download finishes. A file that arrives and fails its
// hash stops at once, because a download did not fail.
export async function downloadVerified(
  url,
  algorithm,
  expected,
  { idleMs = 60_000, ...options } = {},
) {
  const bytes = await withFetchAttempts(
    `Download from ${url}`,
    () => fetchBytes(url, idleMs),
    { delay: outlastOutage, ...options },
  );
  const actual = createHash(algorithm).update(bytes).digest("hex");
  if (actual !== expected)
    throw new Error(
      `Download from ${url} does not match its pinned hash: expected ${algorithm} ${expected}, got ${actual}`,
    );
  return bytes;
}
