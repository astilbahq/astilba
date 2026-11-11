import { request } from "node:http";
import { createServer } from "node:https";
import { connect } from "node:net";

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
  // Terminal sessions upgrade to a WebSocket; pass the upgraded stream through untouched.
  server.on("upgrade", (incoming, socket, head) => {
    if (!allowed(incoming.url)) {
      socket.end(
        "HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n",
      );
      return;
    }
    const upstream = connect(apiPort, "127.0.0.1", () => {
      const lines = [`${incoming.method} ${incoming.url} HTTP/1.1`];
      for (let i = 0; i < incoming.rawHeaders.length; i += 2)
        lines.push(`${incoming.rawHeaders[i]}: ${incoming.rawHeaders[i + 1]}`);
      upstream.write(`${lines.join("\r\n")}\r\n\r\n`);
      if (head.length) upstream.write(head);
      upstream.pipe(socket).pipe(upstream);
    });
    const close = () => {
      upstream.destroy();
      socket.destroy();
    };
    upstream.on("error", close);
    socket.on("error", close);
    upstream.on("close", close);
    socket.on("close", close);
  });
  return server;
}
