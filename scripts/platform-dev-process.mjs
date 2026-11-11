import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  appendFileSync,
  existsSync,
  readFileSync,
  renameSync,
  rmSync,
} from "node:fs";
import { connect, createServer } from "node:net";
import { basename, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

// Runs and supervises the local launcher's child processes. Each child gets its own
// process group, recorded under the lock, so a stop reaches everything it started.
export function supervisor({ root, lock }) {
  const abort = new AbortController();
  const active = new Set();
  const services = new Set();
  let stopping = false;
  let failure;
  let stopped;
  const stopRequested = new Promise((resolve) => {
    stopped = resolve;
  });

  function signalGroup(child, signal) {
    if (!child.pid) return;
    try {
      process.kill(-child.pid, signal);
    } catch (error) {
      // macOS answers EPERM, not ESRCH, for a group whose members have all exited.
      if (error.code !== "ESRCH" && error.code !== "EPERM") throw error;
    }
  }
  // Children marked `last`, such as the tunnel that carries management's database, keep
  // running until everything else has stopped.
  function stop(error) {
    if (stopping) return;
    stopping = true;
    failure = error;
    abort.abort();
    for (const child of new Set([...active, ...services]))
      if (!child.last) signalGroup(child, "SIGTERM");
    stopped();
  }
  async function shutdown() {
    stop();
    const children = [...new Set([...active, ...services])];
    const ended = async (group) => {
      const kill = setTimeout(() => {
        for (const child of group) signalGroup(child, "SIGKILL");
      }, 5000);
      await Promise.all(group.map((child) => child.done));
      clearTimeout(kill);
    };
    await ended(children.filter((child) => !child.last));
    const last = children.filter((child) => child.last);
    for (const child of last) signalGroup(child, "SIGTERM");
    await ended(last);
  }

  function start(
    command,
    args,
    {
      env = process.env,
      cwd = root,
      input,
      inherit = false,
      cleanup = false,
    } = {},
  ) {
    if (stopping && !cleanup) throw new Error("Startup cancelled");
    const child = spawn(command, args, {
      cwd,
      env,
      detached: true,
      stdio: [
        input === undefined ? "ignore" : "pipe",
        inherit ? "inherit" : "pipe",
        inherit ? "inherit" : "pipe",
      ],
    });
    active.add(child);
    // Keep child groups discoverable if SIGKILL prevents our finally block.
    if (child.pid)
      appendFileSync(
        join(lock, "processes"),
        `${child.pid} ${basename(command)}\n`,
      );
    let output = "";
    let errors = "";
    let error;
    child.stdout?.setEncoding("utf8").on("data", (chunk) => {
      output += chunk;
    });
    child.stderr?.setEncoding("utf8").on("data", (chunk) => {
      errors = (errors + chunk).slice(-2000);
    });
    child.on("error", (cause) => {
      error = cause;
    });
    child.stdin?.on("error", () => {});
    child.stdin?.end(input);
    child.done = new Promise((resolve) =>
      child.once("close", (code) => {
        active.delete(child);
        resolve({ code, error, output, errors });
      }),
    );
    return child;
  }
  // Resolves with stdout; rejects with the command and the end of its stderr.
  async function run(command, args, { timeout = 180000, ...options } = {}) {
    const child = start(command, args, options);
    const timer = setTimeout(() => signalGroup(child, "SIGKILL"), timeout);
    try {
      const result = await child.done;
      if (result.error || result.code !== 0)
        throw new Error(
          `${basename(command)} ${args[0] ?? ""} failed${result.error?.code ? ` (${result.error.code})` : ""}${result.errors ? `\n${result.errors.trim()}` : ""}`,
        );
      return result.output.trim();
    } finally {
      clearTimeout(timer);
    }
  }
  // A service that exits on its own stops the whole stack, unless it was replaced.
  function serve(command, args, env, { last = false } = {}) {
    const child = start(command, args, { env, inherit: true });
    child.last = last;
    services.add(child);
    void child.done.then(() => {
      services.delete(child);
      if (!stopping && !child.replaced)
        stop(new Error(`${basename(command)} exited unexpectedly`));
    });
    return child;
  }
  async function replace(child) {
    child.replaced = true;
    signalGroup(child, "SIGTERM");
    await child.done;
  }
  async function waitFor(check, description, attempts = 150) {
    for (let i = 0; i < attempts; i++) {
      abort.signal.throwIfAborted();
      if (await check()) return;
      await delay(200, undefined, { signal: abort.signal });
    }
    throw new Error(`${description} did not become ready`);
  }
  async function httpReady(url, status) {
    try {
      const response = await fetch(url, {
        signal: AbortSignal.any([abort.signal, AbortSignal.timeout(1000)]),
      });
      await response.body?.cancel();
      return response.status === status;
    } catch {
      return false;
    }
  }

  return {
    signal: abort.signal,
    stopRequested,
    get stopping() {
      return stopping;
    },
    get failure() {
      return failure;
    },
    stop,
    shutdown,
    start,
    run,
    serve,
    replace,
    waitFor,
    httpReady,
  };
}

export const tcpReady = (port) =>
  new Promise((resolve) => {
    const socket = connect(port, "127.0.0.1", () => {
      socket.destroy();
      resolve(true);
    });
    socket.on("error", () => resolve(false));
  });

export async function freePort(value) {
  const server = createServer();
  await new Promise((resolve, reject) => {
    server.once("error", () =>
      reject(
        new Error(
          `Port ${value} is in use; stop that preview or choose --port`,
        ),
      ),
    );
    server.listen(value, "127.0.0.1", () => server.close(resolve));
  });
}

export async function anyPort() {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

export const sha256 = (path) =>
  createHash("sha256").update(readFileSync(path)).digest("hex");

// Builds into a side path and keeps the result only when it differs, so an unchanged
// binary never restarts anything.
export async function buildChanged(run, cwd, target, args, env = process.env) {
  const next = `${target}.next`;
  await run("go", ["build", "-trimpath", "-o", next, ...args], {
    cwd,
    env,
    inherit: true,
  });
  if (existsSync(target) && sha256(next) === sha256(target)) {
    rmSync(next);
    return false;
  }
  renameSync(next, target);
  return true;
}
