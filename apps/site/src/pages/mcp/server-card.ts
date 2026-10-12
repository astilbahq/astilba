import { docsMcpGuideUrl, docsMcpUrl, jsonResponse } from "../../discovery";

export const prerender = true;

export const GET = (): Response =>
  jsonResponse(
    {
      $schema:
        "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
      description: "Search and read Astilba's public product documentation.",
      name: "com.astilba/docs",
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
          url: docsMcpUrl,
        },
      ],
      repository: {
        source: "github",
        url: "https://github.com/astilbahq/docs",
      },
      title: "Astilba documentation",
      version: "0.1.0",
      websiteUrl: docsMcpGuideUrl,
    },
    "application/mcp-server-card+json"
  );
