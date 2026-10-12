import {
  docsMcpCardUrl,
  docsMcpGuideUrl,
  docsMcpUrl,
  jsonResponse,
} from "../../discovery";

export const prerender = true;

export const GET = (): Response =>
  jsonResponse(
    {
      linkset: [
        {
          anchor: docsMcpUrl,
          "service-desc": [
            {
              href: docsMcpCardUrl,
              type: "application/mcp-server-card+json",
            },
          ],
          "service-doc": [
            {
              href: docsMcpGuideUrl,
              type: "text/html",
            },
          ],
        },
      ],
    },
    'application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"'
  );
