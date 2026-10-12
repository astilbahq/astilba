import type { ReactNode } from "react";
import { Links, Meta, Outlet, Scripts, ScrollRestoration } from "react-router";
import type { MiddlewareFunction } from "react-router";

import { cacheMiddleware } from "@astilba/cache/react-router";

import { cache } from "./cache";

// The RR-v8 ROOT middleware (§7). Always-on in v8 — registered as the root route module's
// `middleware` export. `cacheMiddleware` provides `cache` into the request lifecycle, opens the
// AsyncLocalStorage frame, and stamps the born-private `Cache-Control: private` default.
export const middleware: MiddlewareFunction[] = [cacheMiddleware({ cache })];

export function Layout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
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
