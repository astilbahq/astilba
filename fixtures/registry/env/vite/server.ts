import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer } from "node:http";
import type { ServerResponse } from "node:http";
import { extname, isAbsolute, relative, resolve, sep } from "node:path";

import { BOOTSTRAP_PROTOCOL } from "@astilba/env/browser";

import { check } from "./.astilba/env/bootstrapDeployment.server.ts";
import { projection } from "./.astilba/env/browser/browser.deployment.ts";

const root = resolve("dist");
const port = Number(process.env.PORT ?? "4173");
const mimeTypes: Readonly<Record<string, string>> = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
};
const localHost = /^(?:localhost|127\.0\.0\.1)(?::(?<port>[1-9][0-9]{0,4}))?$/u;

const sendJson = (
  response: ServerResponse,
  status: number,
  body: Readonly<Record<string, unknown>>
) => {
  response.writeHead(status, {
    "Cache-Control": "private, no-store",
    "content-type": "application/json; charset=utf-8",
  });
  response.end(JSON.stringify(body));
};

const localOriginFromHost = (host: string | undefined): string | undefined => {
  const match = host === undefined ? null : localHost.exec(host);
  const rawPort = match?.groups?.port;
  if (match === null || (rawPort !== undefined && Number(rawPort) > 65_535)) {
    return undefined;
  }
  return new URL(`http://${host}`).origin;
};

const sendFile = async (path: string, response: ServerResponse) => {
  try {
    const file = resolve(root, `.${path === "/" ? "/index.html" : path}`);
    const location = relative(root, file);
    if (
      isAbsolute(location) ||
      location === ".." ||
      location.startsWith(`..${sep}`) ||
      !(await stat(file)).isFile()
    ) {
      throw new Error("not found");
    }
    response.writeHead(200, {
      "content-type": mimeTypes[extname(file)] ?? "application/octet-stream",
    });
    createReadStream(file).pipe(response);
  } catch {
    response.writeHead(404);
    response.end();
  }
};

createServer((request, response) => {
  if (request.url === "/env.json") {
    const checked = check(process.env);
    if (!checked.ok) {
      sendJson(response, 500, { error: checked.diagnostics[0]?.code });
      return;
    }
    const { label } = checked.value;
    const browserOrigin =
      checked.value.browserOrigin ?? localOriginFromHost(request.headers.host);
    if (browserOrigin === undefined) {
      sendJson(response, 500, { error: "ENV_INVALID_VALUE" });
      return;
    }
    sendJson(response, 200, {
      audience: { origin: browserOrigin },
      consumer: projection.consumer,
      contract: projection.contract,
      lifecycle: projection.lifecycle,
      projection: projection.digest,
      protocol: BOOTSTRAP_PROTOCOL,
      values: { label },
    });
    return;
  }
  void sendFile(
    new URL(request.url ?? "/", "http://localhost").pathname,
    response
  );
}).listen(port);
