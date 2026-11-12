// Reaches a port inside a container without publishing it, the way `kubectl
// port-forward` goes through the API server: this process listens on a loopback port
// the kernel gives it, and carries each connection over the Docker API with
// `docker exec … nc`. A published port goes through the VM's port forwarder to the
// same number on the host, where another VM's port can answer instead; this port can
// only ever reach the container named. The container must have `nc`, as the pinned
// PostgreSQL and BusyBox images do.
//
// The listener runs on a worker thread, so a caller blocked in execFileSync, such as a
// fixture running migrations through it, still has its connections carried.
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:net";
import { isMainThread, parentPort, Worker, workerData } from "node:worker_threads";

export async function forward({ host, container, address, port }) {
  const worker = new Worker(new URL(import.meta.url), {
    workerData: { host, container, address, port },
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
  const { host, container, address, port } = workerData;
  const open = new Map();
  const server = createServer((socket) => {
    const child = spawn(
      "docker",
      [
        "--host",
        host,
        "exec",
        "--interactive",
        container,
        "nc",
        address,
        String(port),
      ],
      { stdio: ["pipe", "pipe", "ignore"] },
    );
    open.set(socket, child);
    // nc exits when its input ends, and its output ending closes the connection.
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
