import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";

const port = Math.trunc(Number(process.argv[2] ?? "4173"));
if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) {
  throw new RangeError("The showcase port must be an integer from 1 to 65535.");
}

const root = path.resolve(import.meta.dirname, "../apps/ui-showcase/dist");
const contentTypes = new Map([
  [".css", "text/css; charset=utf-8"],
  [".html", "text/html; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".map", "application/json; charset=utf-8"],
  [".woff2", "font/woff2"],
]);

const server = createServer(async (request, response) => {
  const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
  const relativePath =
    requestUrl.pathname === "/"
      ? "index.html"
      : decodeURIComponent(requestUrl.pathname.slice(1));
  const filePath = path.resolve(root, relativePath);
  const isInsideRoot =
    filePath === root || filePath.startsWith(`${root}${path.sep}`);

  if (!isInsideRoot) {
    response.writeHead(404).end();
    return;
  }

  try {
    const file = await readFile(filePath);
    response.writeHead(200, {
      "Content-Type":
        contentTypes.get(path.extname(filePath)) ?? "application/octet-stream",
    });
    response.end(request.method === "HEAD" ? undefined : file);
  } catch {
    const acceptsDocument =
      request.method === "GET" || request.method === "HEAD";
    const isRoute = path.extname(relativePath) === "";

    if (!(acceptsDocument && isRoute)) {
      response.writeHead(404).end();
      return;
    }

    try {
      const index = await readFile(path.resolve(root, "index.html"));
      response.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
      });
      response.end(request.method === "HEAD" ? undefined : index);
    } catch {
      response.writeHead(404).end();
    }
  }
});

server.listen(port, "127.0.0.1");
