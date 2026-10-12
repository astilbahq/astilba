import type { APIRoute } from "astro";

import { docsUrl, withDocsBase } from "../docs/urls";

export const prerender = true;

export const GET: APIRoute = ({ site }) => {
  const sitemapUrl = site ? docsUrl("/sitemap.xml") : undefined;
  const body = [
    "User-agent: *",
    "Content-Signal: ai-train=yes, search=yes, ai-input=yes",
    `Allow: ${withDocsBase("/")}`,
    sitemapUrl ? "" : undefined,
    sitemapUrl ? `Sitemap: ${sitemapUrl}` : undefined,
    "",
  ]
    .filter((line): line is string => line !== undefined)
    .join("\n");

  return new Response(body, {
    headers: {
      "Content-Type": "text/plain; charset=utf-8",
      "X-Content-Type-Options": "nosniff",
    },
  });
};
