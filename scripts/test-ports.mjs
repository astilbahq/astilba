// Loopback ports for browser suites, a block of ten per run, so suites in other
// checkouts can run at the same time. A run holds its block's first port open for as
// long as it lives, and that listener is the lock: the kernel releases it when the run
// exits, however it exits. The other nine ports are left free for the run's servers.
//
// Blocks sit below the default ephemeral ranges (32768 on Linux, 49152 on macOS), so an
// outgoing connection never takes a port between the check here and a server's bind.
import {
  linkSync,
  mkdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { dirname } from "node:path";

export const blockSize = 10;
export const firstBlock = 15180;
export const lastBlock = 15990;

const held = [];

function listen(port, host) {
  return new Promise((resolve) => {
    const server = createServer();
    server.once("error", (error) =>
      resolve(
        // A machine without IPv6 has no ::1 to take.
        host === "::1" &&
          (error.code === "EADDRNOTAVAIL" || error.code === "EAFNOSUPPORT")
          ? "absent"
          : undefined,
      ),
    );
    server.listen({ port, host }, () => resolve(server));
  });
}

function close(server) {
  return new Promise((resolve) => server.close(() => resolve()));
}

async function free(port) {
  for (const host of ["127.0.0.1", "::1"]) {
    const server = await listen(port, host);
    if (!server) return false;
    if (server !== "absent") await close(server);
  }
  return true;
}

// reservePortBlock returns the first port of a block this process now holds. Port
// base+1 to base+9 are free for its servers when it returns; the caller binds them.
export async function reservePortBlock({
  first = firstBlock,
  last = lastBlock,
} = {}) {
  for (let base = first; base <= last; base += blockSize) {
    const lock = await listen(base, "127.0.0.1");
    if (!lock) continue;
    let ready = true;
    for (let port = base + 1; ready && port < base + blockSize; port++)
      ready = await free(port);
    if (ready) {
      // The block stays this process's until it exits; it never keeps it running.
      lock.unref();
      held.push(lock);
      return base;
    }
    await close(lock);
  }
  throw new Error(
    `No free block of ${blockSize} loopback ports between ${first} and ${last + blockSize - 1}`,
  );
}

// holdCheckout refuses a second run of a suite in one checkout, where both would build
// into and clean the same directories. Separate checkouts each hold their own lock. The
// file names the process holding it, and one whose process has exited is taken over.
export function holdCheckout(path, suite) {
  mkdirSync(dirname(path), { recursive: true });
  // The lock appears with its holder already written, so a reader never sees it empty.
  const claim = `${path}.${process.pid}`;
  writeFileSync(claim, String(process.pid), { mode: 0o600 });
  try {
    for (let attempt = 0; ; attempt++) {
      try {
        linkSync(claim, path);
        break;
      } catch (error) {
        if (error.code !== "EEXIST") throw error;
      }
      const holder = Number(readFileSync(path, "utf8"));
      // A second refusal means another run took over the same stale lock first.
      if (alive(holder) || attempt > 0)
        throw new Error(
          `${suite} is already running in this checkout (process ${holder}); wait for it, or run the other from a separate checkout`,
        );
      rmSync(path, { force: true });
    }
  } finally {
    rmSync(claim, { force: true });
  }
  process.once("exit", () => {
    try {
      if (Number(readFileSync(path, "utf8")) === process.pid) rmSync(path);
    } catch {}
  });
}

function alive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error.code === "EPERM";
  }
}

// The dashboard fixture's servers in a block. Its configuration, fixture and tests all
// read their ports from here.
export function dashboardPorts(base = portBlock()) {
  return {
    // Management, which the browser and most tests use.
    api: base + 1,
    // Vite, for dev-only localisation, and the management server it proxies to.
    dev: base + 2,
    devApi: base + 3,
    // The Git provider and GitHub OAuth fixtures; GitHub's also listens on ::1.
    gitProvider: base + 4,
    github: base + 5,
    // The hosted installation's HTTPS proxy and the management server behind it.
    hosted: base + 6,
    hostedBackend: base + 7,
  };
}

// portBlock reads the block a run's main process reserved, from the variable it set
// before starting servers and workers, which inherit it.
export function portBlock(environment = process.env) {
  const value = environment.PLAT_BROWSER_PORT_BASE ?? "";
  const base = Number(value);
  if (
    !/^\d+$/.test(value) ||
    base < 1024 ||
    base + blockSize > 65536 ||
    base % blockSize !== 0
  )
    throw new Error(
      "PLAT_BROWSER_PORT_BASE must name a reserved block; run the suite through Playwright",
    );
  return base;
}
