export const prerender = true;

export const GET = (): Response =>
  new Response(
    `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://astilba.com/</loc></url><url><loc>https://astilba.com/env/</loc></url><url><loc>https://astilba.com/cache/</loc></url></urlset>\n`,
    { headers: { "Content-Type": "application/xml; charset=utf-8" } }
  );
