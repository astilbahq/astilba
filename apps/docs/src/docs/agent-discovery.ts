import { SUPPORTED_PROTOCOL_VERSIONS } from "@modelcontextprotocol/sdk/types.js";

import { docsUrl, withDocsBase } from "./urls.ts";

export const API_CATALOG_PATH = withDocsBase("/.well-known/api-catalog");
export const API_CATALOG_LINK_VALUE = `<${API_CATALOG_PATH}>; rel="api-catalog"; type="application/linkset+json"`;
export const MCP_CATALOG_PATH = withDocsBase("/.well-known/mcp/catalog.json");
export const MCP_COMPATIBILITY_CARD_PATH = withDocsBase(
  "/.well-known/mcp/server-card.json"
);
export const MCP_ENDPOINT_PATH = withDocsBase("/mcp");
export const MCP_SERVER_CARD_PATH = withDocsBase("/mcp/server-card");
const MCP_USAGE_PATH = withDocsBase("/agents/mcp/");

const absoluteUrl = (path: string): string => docsUrl(path);

export const MCP_SERVER_INFO = Object.freeze({
  description: "Search and read Astilba's public product documentation.",
  name: "com.astilba/docs",
  title: "Astilba documentation",
  version: "0.1.0",
  websiteUrl: absoluteUrl(MCP_USAGE_PATH),
});

export const MCP_SERVER_INSTRUCTIONS =
  "Use search_docs to find relevant public pages, then read the linked resource or use read_doc for a bounded chunk. Before making release or availability claims, use Env's Release and support page for Env claims and Cache's Implementation status page for Cache claims. This server is public and read-only.";

export const MCP_ENDPOINT_URL = absoluteUrl(MCP_ENDPOINT_PATH);
export const MCP_SERVER_CARD_URL = absoluteUrl(MCP_SERVER_CARD_PATH);
export const MCP_USAGE_URL = absoluteUrl(MCP_USAGE_PATH);

export const createMcpServerCard = () => ({
  $schema:
    "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
  description: MCP_SERVER_INFO.description,
  name: MCP_SERVER_INFO.name,
  remotes: [
    {
      supportedProtocolVersions: [...SUPPORTED_PROTOCOL_VERSIONS],
      type: "streamable-http" as const,
      url: MCP_ENDPOINT_URL,
    },
  ],
  repository: {
    source: "github",
    url: "https://github.com/astilbahq/docs",
  },
  title: MCP_SERVER_INFO.title,
  version: MCP_SERVER_INFO.version,
  websiteUrl: MCP_SERVER_INFO.websiteUrl,
});

export const createMcpCatalog = () => ({
  entries: [
    {
      identifier: "urn:air:astilba.com:docs",
      type: "application/mcp-server-card+json",
      url: MCP_SERVER_CARD_URL,
    },
  ],
  specVersion: "draft",
});

export const createMcpCompatibilityCard = () => ({
  capabilities: {
    resources: {},
    tools: {},
  },
  description: MCP_SERVER_INFO.description,
  documentationUrl: MCP_USAGE_URL,
  serverInfo: {
    name: MCP_SERVER_INFO.name,
    version: MCP_SERVER_INFO.version,
  },
  transport: {
    endpoint: MCP_ENDPOINT_URL,
    type: "streamable-http",
  },
});

export const createApiCatalog = () => ({
  linkset: [
    {
      anchor: MCP_ENDPOINT_URL,
      "service-desc": [
        {
          href: MCP_SERVER_CARD_URL,
          type: "application/mcp-server-card+json",
        },
      ],
      "service-doc": [
        {
          href: MCP_USAGE_URL,
          type: "text/html",
        },
      ],
    },
  ],
});
