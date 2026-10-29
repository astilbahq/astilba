/// <reference types="@cloudflare/workers-types" />

// This adapter only serves static assets and forwards the fixed API origin.
interface Bindings {
  ASSETS: Fetcher;
  API_ORIGIN: string;
  DEVELOPMENT?: string;
}
export default {
  async fetch(request: Request, env: Bindings): Promise<Response> {
    const incoming = new URL(request.url);
    if (
      incoming.pathname === "/onboarding/network-prepare.sh" ||
      [
        "/api/",
        "/agent/v1/",
        "/agent-artifacts/",
        "/agent-public-key.pem",
        "/onboarding/bootstrap.sh",
        "/hooks/git/",
        "/hooks/stripe",
        "/hooks/github-app",
      ].some((prefix) => incoming.pathname.startsWith(prefix))
    ) {
      let upstream: URL;
      try {
        upstream = new URL(env.API_ORIGIN);
      } catch {
        return new Response("API origin is not configured", { status: 503 });
      }
      if (
        upstream.username ||
        upstream.password ||
        upstream.pathname !== "/" ||
        upstream.search ||
        upstream.hash ||
        (upstream.protocol !== "https:" &&
          !(
            env.DEVELOPMENT === "true" &&
            upstream.protocol === "http:" &&
            ["127.0.0.1", "localhost", "[::1]"].includes(upstream.hostname)
          ))
      )
        return new Response("Invalid API origin", { status: 503 });
      upstream.pathname = incoming.pathname;
      upstream.search = incoming.search;
      const headers = new Headers(request.headers);
      headers.delete("host");
      headers.delete("x-forwarded-for");
      headers.delete("forwarded");
      const clientIP = request.headers.get("cf-connecting-ip");
      if (clientIP) headers.set("x-forwarded-for", clientIP);
      try {
        return await fetch(
          new Request(upstream, {
            method: request.method,
            headers,
            body: ["GET", "HEAD"].includes(request.method)
              ? undefined
              : request.body,
            redirect: "manual",
          }),
        );
      } catch {
        return new Response("API unavailable", {
          status: 502,
          headers: { "Cache-Control": "no-store" },
        });
      }
    }
    let response = await env.ASSETS.fetch(request);
    if (
      [206, 304].includes(response.status) &&
      response.headers.has("Content-Type") &&
      !isHTML(response)
    )
      return response;
    if ([206, 304, 412, 416].includes(response.status)) {
      // Asset validators and byte ranges describe the shell before its nonce.
      const headers = new Headers(request.headers);
      for (const name of [
        "If-Match",
        "If-None-Match",
        "If-Modified-Since",
        "If-Unmodified-Since",
        "If-Range",
        "Range",
      ])
        headers.delete(name);
      const full = await env.ASSETS.fetch(new Request(request, { headers }));
      if (isHTML(full)) {
        await response.body?.cancel();
        response = full;
      } else {
        await full.body?.cancel();
      }
    }
    if (!isHTML(response)) return response;

    const nonce = btoa(
      String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16))),
    );
    const headers = new Headers(response.headers);
    headers.set(
      "Content-Security-Policy",
      `default-src 'self'; script-src 'self'; style-src 'self' 'nonce-${nonce}'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'`,
    );
    headers.set("X-Content-Type-Options", "nosniff");
    headers.set("Referrer-Policy", "no-referrer");
    headers.set("Cache-Control", "no-store");
    for (const name of [
      "ETag",
      "Last-Modified",
      "Content-Length",
      "Content-Range",
      "Accept-Ranges",
    ])
      headers.delete(name);
    const init = {
      status: response.status,
      statusText: response.statusText,
      headers,
    };
    if (request.method === "HEAD") {
      await response.body?.cancel();
      return new Response(null, init);
    }
    const secured = new Response(response.body, init);
    return new HTMLRewriter()
      .on("head", {
        element(head) {
          head.prepend(`<meta name="style-nonce" content="${nonce}">`, {
            html: true,
          });
        },
      })
      .transform(secured);
  },
};

function isHTML(response: Response): boolean {
  return (
    response.headers
      .get("Content-Type")
      ?.split(";", 1)[0]
      .trim()
      .toLowerCase() === "text/html"
  );
}
