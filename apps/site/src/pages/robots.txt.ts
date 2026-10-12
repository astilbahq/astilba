export const prerender = true;

export const GET = (): Response =>
  new Response(
    [
      "User-agent: *",
      "Allow: /",
      "",
      "Sitemap: https://astilba.com/sitemap.xml",
      "",
    ].join("\n"),
    { headers: { "Content-Type": "text/plain; charset=utf-8" } }
  );
