// Reaches a port inside a container without publishing it, the way `kubectl
// port-forward` goes through the API server: this process listens on a loopback port
// the kernel gives it, and carries each connection over the Docker API by running
// services/platform/internal/integration/relay in the container with `docker exec`.
// A published port goes through the VM's port forwarder to the same number on the
// host, where another VM's port can answer instead; this port can only ever reach the
// container named. The relay is built for the daemon's architecture and copied into
// the container, which needs only a writable /tmp.
//
// The listener runs on a worker thread, so a caller blocked in execFileSync, such as a
// fixture running migrations through it, still has its connections carried.
import { execFileSync, spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { isMainThread, parentPort, Worker, workerData } from "node:worker_threads";

const relayPath = "/tmp/astilba-relay";
const relays = new Map();

// relay builds the relay for the daemon's architecture, once per process.
function relay(host) {
  const arch = execFileSync(
    "docker",
    ["--host", host, "version", "--format", "{{.Server.Arch}}"],
    { encoding: "utf8" },
  ).trim();
  if (!relays.has(arch)) {
    const directory = mkdtempSync(join(tmpdir(), "astilba-relay-"));
    process.once("exit", () =>
      rmSync(directory, { recursive: true, force: true }),
    );
    const binary = join(directory, "relay");
    execFileSync(
      "go",
      ["build", "-trimpath", "-o", binary, "./internal/integration/relay"],
      {
        cwd: resolve(import.meta.dirname, "../services/platform"),
        env: { ...process.env, CGO_ENABLED: "0", GOOS: "linux", GOARCH: arch },
        stdio: ["ignore", "ignore", "inherit"],
      },
    );
    relays.set(arch, binary);
  }
  return relays.get(arch);
}

export async function forward({ host, container, address, port }) {
  execFileSync("docker", [
    "--host",
    host,
    "cp",
    relay(host),
    `${container}:${relayPath}`,
  ]);
  const worker = new Worker(new URL(import.meta.url), {
    workerData: { host, container, target: `${address}:${port}` },
  });
  // Not events.once, which would also reject, unhandled, if the worker failed.
  const exited = new Promise((resolve) => worker.once("exit", resolve));
  const [message] = await Promise.race([
    once(worker, "message"),
    once(worker, "error").then(([error]) => {
      throw error;
    }),
  ]);
  return {
    port: message.port,
    // Closing ends the connections still open, as removing the container would.
    close: async () => {
      worker.postMessage("close");
      await exited;
    },
  };
}

if (!isMainThread && workerData?.container) {
  const { host, container, target } = workerData;
  const open = new Map();
  const server = createServer((socket) => {
    const child = spawn(
      "docker",
      ["--host", host, "exec", "--interactive", container, relayPath, target],
      { stdio: ["pipe", "pipe", "ignore"] },
    );
    open.set(socket, child);
    // The relay passes on the end of the client's input, and its output ending, when
    // the server closes, closes the connection.
    socket.pipe(child.stdin);
    child.stdout.pipe(socket);
    child.stdin.on("error", () => {});
    socket.on("error", () => child.stdin.destroy());
    socket.once("close", () => {
      open.delete(socket);
      child.stdin.end();
    });
    child.on("error", () => socket.destroy());
  });
  server.listen(0, "127.0.0.1", () =>
    parentPort.postMessage({ port: server.address().port }),
  );
  parentPort.on("message", () => {
    server.close();
    for (const [socket, child] of open) {
      socket.destroy();
      child.kill();
    }
    parentPort.close();
  });
}
