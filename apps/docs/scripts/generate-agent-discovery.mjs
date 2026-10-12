import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

import {
  API_CATALOG_PATH,
  createApiCatalog,
  createMcpCatalog,
  createMcpCompatibilityCard,
  createMcpServerCard,
  MCP_CATALOG_PATH,
  MCP_COMPATIBILITY_CARD_PATH,
  MCP_SERVER_CARD_PATH,
} from "../src/docs/agent-discovery.ts";
import { ASTILBA_ORIGIN } from "../src/docs/urls.ts";

const siteValue = process.env.ASTILBA_DOCS_SITE;
let site;

try {
  site = siteValue ? new URL(siteValue) : undefined;
} catch {
  site = undefined;
}

if (
  !site ||
  site.origin !== ASTILBA_ORIGIN ||
  site.username ||
  site.password ||
  site.pathname !== "/" ||
  site.search ||
  site.hash
) {
  throw new Error(
    `[agent-discovery] ASTILBA_DOCS_SITE must use the canonical origin ${ASTILBA_ORIGIN}.`
  );
}

const dist = resolve(process.cwd(), "dist");
const artifacts = new Map([
  [API_CATALOG_PATH, createApiCatalog()],
  [MCP_CATALOG_PATH, createMcpCatalog()],
  [MCP_COMPATIBILITY_CARD_PATH, createMcpCompatibilityCard()],
  [MCP_SERVER_CARD_PATH, createMcpServerCard()],
]);

for (const [path, value] of artifacts) {
  const outputPath = resolve(dist, path.replace(/^\/+/, ""));
  await mkdir(dirname(outputPath), { recursive: true });
  await writeFile(outputPath, `${JSON.stringify(value, null, 2)}\n`, "utf8");
}

console.log(
  `[agent-discovery] Generated ${artifacts.size} discovery artifacts.`
);
