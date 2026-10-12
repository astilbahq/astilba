import { docsMcpCardUrl, jsonResponse } from "../../../discovery";

export const prerender = true;

export const GET = (): Response =>
  jsonResponse({
    entries: [
      {
        identifier: "urn:air:astilba.com:docs",
        type: "application/mcp-server-card+json",
        url: docsMcpCardUrl,
      },
    ],
    specVersion: "draft",
  });
