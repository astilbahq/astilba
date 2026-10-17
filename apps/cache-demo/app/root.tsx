import { cacheMiddleware } from "@astilba/cache/react-router";
import type { ReactNode } from "react";
import { Links, Meta, Outlet, Scripts, ScrollRestoration } from "react-router";

import type { Route } from "./+types/root";
import { waitUntilContext, worldContext } from "./world";

// The RR-v8 ROOT middleware, always-on. Unlike the build-smoke fixture — which registers
// `cacheMiddleware({ cache })` against a module-scope, driverless cache — this app's cache is built
// per isolate from the REAL bindings, so it arrives through the router context the worker seeds.
// The wrapper is one line, and it is the seam that keeps every route file free of the Workers
// runtime's types: the worker knows what a KV namespace is; the app knows what a `Cache` is.
//
// The middleware provides the cache into the request lifecycle, opens the AsyncLocalStorage frame
// a `getOrSet` call site reaches `currentRequest()` through, binds this request's render collector,
// and stamps the born-private `Cache-Control: private` default. The demo's telemetry sink is
// attached here — see `app/world.ts` for which events that does and does not reach.
export const middleware: Route.MiddlewareFunction[] = [
  async (args, next) => {
    const world = args.context.get(worldContext);
    const result: unknown = await cacheMiddleware({
      // The cache THIS request talks to — the isolate's long-lived one, or the reader an armed
      // episode booted for itself (see `app/world.ts`). The middleware binds the render collector to
      // whichever it is handed, so asking the world per request is what keeps the dependency row
      // describing the cache the page is actually reading.
      cache: world.activeCache(),
      telemetry: world.sink,
      // THIS request's `waitUntil`, seeded by the worker from its own `ExecutionContext` (see
      // `workers/app.ts`). Passed per call, so the poll tick the middleware fires rides the execution
      // context of the request that fired it — held open past the response instead of cancelled at
      // teardown. Never captured: it is read fresh from the request context every request.
      waitUntil: args.context.get(waitUntilContext),
    })(args, next);
    // `cacheMiddleware` is typed against react-router's own `MiddlewareFunction`, whose result
    // parameter defaults to `unknown`. The route-generated annotation is narrower — `void |
    // Response` — so the adapter's own type does not satisfy it, and an app doing typed routes
    // has to put the response back through a narrowing it should not need to write. The adapter
    // does return the response it decided headers on, so this is a true narrowing rather than a
    // cast; it is written out here instead of being dodged (by falling back to the untyped
    // `MiddlewareFunction[]`, as the build-smoke fixture does) because the friction is real and
    // hiding it would mean the demo never found it.
    return result instanceof Response ? result : undefined;
  },
];

export function Layout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <title>astilba cache — chaos demo</title>
        <Meta />
        <Links />
      </head>
      <body>
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export default function App() {
  return <Outlet />;
}
