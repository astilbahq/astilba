export const prerender = true;

export const GET = (): Response =>
  new Response(
    `<?xml version="1.0" encoding="UTF-8"?>\n<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>https://astilba.com/sitemap-site.xml</loc></sitemap><sitemap><loc>https://astilba.com/docs/sitemap.xml</loc></sitemap></sitemapindex>\n`,
    { headers: { "Content-Type": "application/xml; charset=utf-8" } }
  );
