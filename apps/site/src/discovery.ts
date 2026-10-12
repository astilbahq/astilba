export const docsMcpUrl = "https://astilba.com/docs/mcp";
export const docsMcpGuideUrl = "https://astilba.com/docs/agents/mcp/";
export const docsMcpCardUrl = "https://astilba.com/docs/mcp/server-card";

export const jsonResponse = (
  value: unknown,
  contentType = "application/json; charset=utf-8"
): Response =>
  new Response(`${JSON.stringify(value, undefined, 2)}\n`, {
    headers: { "Content-Type": contentType },
  });

export const mcpServerCard = {
  capabilities: {
    resources: {},
    tools: {},
  },
  description: "Search and read Astilba's public product documentation.",
  documentationUrl: docsMcpGuideUrl,
  serverInfo: {
    name: "com.astilba/docs",
    version: "0.1.0",
  },
  transport: {
    endpoint: docsMcpUrl,
    type: "streamable-http",
  },
} as const;
