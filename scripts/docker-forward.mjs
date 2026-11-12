// Reaches a port inside a container without publishing it, the way `kubectl
// port-forward` goes through the API server: this process listens on a loopback port
// the kernel gives it, and carries each connection over the Docker API with
// `docker exec … nc`. A published port goes through the VM's port forwarder to the
// same number on the host, where another VM's port can answer instead; this port can
// only ever reach the container named. The container must have `nc`, as the pinned
// PostgreSQL and BusyBox images do.
import { spawn } from "node:child_process";
import { createServer } from "node:net";

export async function forward({ host, container, address, port }) {
  const open = new Set();
  const server = createServer((socket) => {
    open.add(socket);
    socket.once("close", () => open.delete(socket));
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
    // nc exits when its input ends, and its output ending closes the connection.
    socket.pipe(child.stdin);
    child.stdout.pipe(socket);
    child.stdin.on("error", () => {});
    socket.on("error", () => child.stdin.destroy());
    socket.on("close", () => child.stdin.end());
    child.on("error", () => socket.destroy());
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return {
    port: server.address().port,
    // Closing ends the connections still open, as removing the container would.
    close: () =>
      new Promise((resolve) => {
        server.close(() => resolve());
        for (const socket of open) socket.destroy();
      }),
  };
}
