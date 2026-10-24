// This adapter only serves static assets and forwards the fixed API origin.
interface Bindings {
  ASSETS: { fetch(request: Request): Promise<Response> };
  API_ORIGIN: string;
  DEVELOPMENT?: string;
}
export default {
  async fetch(request: Request, env: Bindings): Promise<Response> {
    const incoming = new URL(request.url);
    if (
      [
        "/api/",
        "/agent/v1/",
        "/agent-artifacts/",
        "/hooks/git/",
        "/hooks/stripe",
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
    return env.ASSETS.fetch(request);
  },
};
