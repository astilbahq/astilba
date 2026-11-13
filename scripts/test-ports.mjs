// Loopback ports for browser suites, a block of ten per run, so suites in other
// checkouts can run at the same time. A run holds its block's first port open for as
// long as it lives, and that listener is the lock: the kernel releases it when the run
// exits, however it exits. The other nine ports are left free for the run's servers.
//
// Blocks sit below the default ephemeral ranges (32768 on Linux, 49152 on macOS), so an
// outgoing connection never takes a port between the check here and a server's bind.
import { createHash } from "node:crypto";
import { mkdirSync, statSync } from "node:fs";
import { connect, createServer } from "node:net";
import { join, resolve } from "node:path";

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
// into the same directories; separate checkouts each have their own. The lock is a Unix
// socket this process listens on, so the kernel releases it with the process, however
// it ends. A killed run leaves its socket file, which answers no one; the next run
// moves on to the next numbered path rather than removing it, because removing a path
// could remove the socket another run has just made there. Binding is atomic, so of
// runs starting together only one takes each path. It returns a release function.
export async function holdCheckout(checkout, suite) {
  const key = createHash("sha256")
    .update(resolve(checkout))
    .digest("hex")
    .slice(0, 16);
  // A socket path must stay under about 100 bytes, which TMPDIR need not, so the lock
  // lives in a directory of this user's under /tmp, as tmux's do.
  const directory = `/tmp/astilba-${process.getuid()}`;
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const owner = statSync(directory);
  if (owner.uid !== process.getuid() || (owner.mode & 0o077) !== 0)
    throw new Error(`${directory} must belong to this user alone`);
  for (let n = 0, tries = 0; n < 100 && tries < 200; tries++) {
    const path = join(directory, `suite-${key}-${n}.sock`);
    const lock = createServer((connection) => connection.destroy());
    const taken = await new Promise((resolve, reject) => {
      lock.once("error", (error) =>
        error.code === "EADDRINUSE" ? resolve(false) : reject(error),
      );
      lock.listen(path, () => resolve(true));
    });
    if (taken) {
      lock.unref();
      return () => close(lock);
    }
    const state = await probe(path);
    if (state === "held")
      throw new Error(
        `${suite} is already running in this checkout; wait for it, or run the other from a separate checkout`,
      );
    // Past a killed run's socket, try the next path. A holder that released between
    // the bind and the probe has freed this one, so try it again.
    if (state === "stale") n++;
  }
  throw new Error(
    `Cannot take a lock in ${directory} past the sockets killed runs left; remove its suite-${key}-* files when no suite is running`,
  );
}

// probe says whether a lock path is held, left by a killed run (nothing listens), or
// gone.
function probe(path) {
  return new Promise((resolve) => {
    const socket = connect(path);
    socket.once("connect", () => {
      socket.destroy();
      resolve("held");
    });
    socket.once("error", (error) =>
      resolve(error.code === "ENOENT" ? "gone" : "stale"),
    );
  });
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
