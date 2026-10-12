interface AssetsBinding {
  fetch: (request: Request) => Promise<Response>;
}

interface Env {
  ASSETS: AssetsBinding;
}

const redirectHeaders = {
  "Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
  "Referrer-Policy": "strict-origin-when-cross-origin",
  "Strict-Transport-Security": "max-age=31536000",
  "X-Content-Type-Options": "nosniff",
} as const;

const redirectDocsIndex = (url: URL): Response => {
  const target = new URL(url);
  target.pathname = "/docs/";

  return new Response(null, {
    headers: {
      ...redirectHeaders,
      Location: target.href,
    },
    status: 308,
  });
};

export default {
  fetch(request: Request, env: Env): Promise<Response> | Response {
    const url = new URL(request.url);

    if (url.pathname === "/docs") {
      return redirectDocsIndex(url);
    }

    return env.ASSETS.fetch(request);
  },
};
