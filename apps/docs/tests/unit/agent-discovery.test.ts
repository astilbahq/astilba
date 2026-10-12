import { describe, expect, it } from "vitest";

import {
  API_CATALOG_PATH,
  createApiCatalog,
  createMcpCatalog,
  createMcpCompatibilityCard,
  createMcpServerCard,
  MCP_CATALOG_PATH,
  MCP_COMPATIBILITY_CARD_PATH,
  MCP_ENDPOINT_URL,
  MCP_SERVER_CARD_PATH,
  MCP_SERVER_CARD_URL,
  MCP_SERVER_INFO,
  MCP_USAGE_URL,
} from "../../src/docs/agent-discovery";

describe("agent discovery metadata", () => {
  it("keeps the runtime identity and public paths stable", () => {
    expect({
      apiCatalog: API_CATALOG_PATH,
      compatibilityCard: MCP_COMPATIBILITY_CARD_PATH,
      mcpCatalog: MCP_CATALOG_PATH,
      serverCard: MCP_SERVER_CARD_PATH,
    }).toEqual({
      apiCatalog: "/docs/.well-known/api-catalog",
      compatibilityCard: "/docs/.well-known/mcp/server-card.json",
      mcpCatalog: "/docs/.well-known/mcp/catalog.json",
      serverCard: "/docs/mcp/server-card",
    });
    expect(MCP_SERVER_INFO).toEqual({
      description: "Search and read Astilba's public product documentation.",
      name: "com.astilba/docs",
      title: "Astilba documentation",
      version: "0.1.0",
      websiteUrl: "https://astilba.com/docs/agents/mcp/",
    });
  });

  it("publishes the exact experimental MCP catalog projection", () => {
    const catalog = createMcpCatalog();

    expect(catalog).toEqual({
      entries: [
        {
          identifier: "urn:air:astilba.com:docs",
          type: "application/mcp-server-card+json",
          url: MCP_SERVER_CARD_URL,
        },
      ],
      specVersion: "draft",
    });
    expect(catalog.entries[0]).not.toHaveProperty("displayName");
    expect(catalog.entries[0]).not.toHaveProperty("mediaType");
  });

  it("keeps the canonical card aligned with the live MCP transport", () => {
    const card = createMcpServerCard();

    expect(card).toEqual({
      $schema:
        "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
      description: MCP_SERVER_INFO.description,
      name: MCP_SERVER_INFO.name,
      remotes: [
        {
          supportedProtocolVersions: [
            "2025-11-25",
            "2025-06-18",
            "2025-03-26",
            "2024-11-05",
            "2024-10-07",
          ],
          type: "streamable-http",
          url: MCP_ENDPOINT_URL,
        },
      ],
      repository: {
        source: "github",
        url: "https://github.com/astilbahq/docs",
      },
      title: MCP_SERVER_INFO.title,
      version: MCP_SERVER_INFO.version,
      websiteUrl: MCP_USAGE_URL,
    });
    expect(card).not.toHaveProperty("capabilities");
    expect(card).not.toHaveProperty("tools");
    expect(card).not.toHaveProperty("resources");
  });

  it("keeps the legacy checker projection truthful and separate", () => {
    expect(createMcpCompatibilityCard()).toEqual({
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
  });

  it("publishes only the existing read-only MCP endpoint in the API catalog", () => {
    const catalog = createApiCatalog();

    expect(catalog).toEqual({
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
    expect(catalog.linkset[0]).not.toHaveProperty("status");
  });
});
