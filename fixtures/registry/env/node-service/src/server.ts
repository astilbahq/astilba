import { createServer } from "node:http";

import { check } from "../.astilba/env/serviceDeployment.server.ts";

const port = Number(process.env.PORT ?? "3101");
const server = createServer((_, response) => {
  const checked = check(process.env);
  if (!checked.ok) {
    response.writeHead(500, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: checked.diagnostics[0]?.code }));
    return;
  }
  const { apiOrigin } = checked.value;
  response.writeHead(200, { "content-type": "application/json" });
  response.end(JSON.stringify({ configured: Boolean(apiOrigin) }));
});

server.listen(port);
