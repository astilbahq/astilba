import { request } from "node:http";
import { createServer } from "node:https";

// The agent only speaks HTTPS to management, and the local API serves loopback HTTP. This
// proxy terminates TLS with the stack's own certificate, which only the VM trusts, and
// forwards just the paths an agent uses. The browser API stays out of the VM's reach.
const agentPaths = /^\/(agent\/v1\/|agent-artifacts\/|agent-public-key\.pem$)/;

// Agent paths hold only UUIDs and hex digests, so anything encoded or dotted is refused
// rather than normalised differently here and in management.
export function allowed(url) {
  const path = url.split("?")[0];
  return (
    /^[A-Za-z0-9/_.-]+$/.test(path) &&
    !path.includes("/.") &&
    agentPaths.test(path)
  );
}

export function agentProxy({ key, cert, apiPort }) {
  const server = createServer(
    { key, cert, minVersion: "TLSv1.2" },
    (incoming, outgoing) => {
      if (!allowed(incoming.url)) {
        outgoing.writeHead(404).end();
        incoming.resume();
        return;
      }
      const upstream = request(
        {
          host: "127.0.0.1",
          port: apiPort,
          path: incoming.url,
          method: incoming.method,
          headers: incoming.headers,
        },
        (response) => {
          outgoing.writeHead(response.statusCode, response.headers);
          response.pipe(outgoing);
        },
      );
      upstream.on("error", () => {
        if (!outgoing.headersSent) outgoing.writeHead(502);
        outgoing.end();
      });
      incoming.on("aborted", () => upstream.destroy());
      incoming.pipe(upstream);
    },
  );
  // Terminal sessions upgrade to a WebSocket. The two sockets are joined only once
  // management has switched protocols, so nothing pipelined behind the upgrade request
  // reaches management without passing the path check. Any other answer ends the
  // connection.
  server.on("upgrade", (incoming, socket, head) => {
    socket.on("error", () => socket.destroy());
    if (!allowed(incoming.url) || head.length) {
      socket.end(
        "HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n",
      );
      return;
    }
    const upstream = request({
      host: "127.0.0.1",
      port: apiPort,
      path: incoming.url,
      method: incoming.method,
      headers: incoming.headers,
    });
    upstream.on("upgrade", (response, connection, upgradeHead) => {
      const lines = [`HTTP/1.1 101 ${response.statusMessage}`];
      for (let i = 0; i < response.rawHeaders.length; i += 2)
        lines.push(`${response.rawHeaders[i]}: ${response.rawHeaders[i + 1]}`);
      socket.write(`${lines.join("\r\n")}\r\n\r\n`);
      if (upgradeHead.length) socket.write(upgradeHead);
      const close = () => {
        connection.destroy();
        socket.destroy();
      };
      connection.on("error", close);
      connection.on("close", close);
      socket.on("close", close);
      connection.pipe(socket).pipe(connection);
    });
    upstream.on("response", (response) => {
      response.resume();
      socket.end(
        `HTTP/1.1 ${response.statusCode} ${response.statusMessage}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`,
      );
    });
    upstream.on("error", () => socket.destroy());
    socket.on("close", () => upstream.destroy());
    upstream.end();
  });
  return server;
}
