import { createRequestHandler } from "react-router";

// The Cloudflare Workers entry (wrangler `main`). @cloudflare/vite-plugin bundles it during
// `react-router build`; it serves the RR framework server build. `loadContext` is omitted (RR v8's
// `RequestHandler` makes it optional; the born-private middleware needs no seeded context here).
const handler = createRequestHandler(
  () => import("virtual:react-router/server-build"),
  import.meta.env.MODE
);

export default {
  fetch(request: Request): Promise<Response> {
    return handler(request);
  },
};
