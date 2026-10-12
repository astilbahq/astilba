---
title: Next.js
description: Wire generated Env targets and browser projections into Next.js App Router or Pages Router applications.
---

Next.js integration is application-owned wiring around generated Env modules. There is no `@astilba/env/next` export and no framework-specific configuration semantics.

Use a generated server target wherever Next.js runs server code. When the browser needs deployment configuration, choose one of two application-owned delivery modes:

- return the public envelope from a same-origin JSON route and load it asynchronously with `loadBrowserBootstrap`; or
- transport the exact envelope as inert, safely escaped serialized JSON through Next.js and parse it synchronously with `parseBrowserBootstrap`.

Both modes use the same generated public projection and validate the same envelope identity. Neither turns the Env envelope into executable JavaScript, writes a `window` global, or interpolates unescaped JSON.

`@astilba/env` 0.3.0 has package-consumer evidence for Next.js 15.5.22 and 16.2.12 across App Router static, App Router request, Pages Router static, and Pages Router request modes. The server side of that evidence uses the Node.js runtime.

The maintained independent-pnpm example invokes Next's webpack builder because default Turbopack cannot resolve the exact-registry dependency from that repository fixture layout. The package-consumer matrix also validates a default `next build`; this is not a general Env or Turbopack incompatibility.

The Next.js 16 evidence and the maintained examples use the previous caching model with Cache Components disabled. This page does not claim support for `cacheComponents: true`; `dynamic = "force-dynamic"` belongs to that previous model.

This page does not claim support for Next.js Edge Runtime or a Next.js deployment on Cloudflare Workers. The separate [Cloudflare Workers](/docs/env/cloudflare-workers/) boundary admits only the documented direct Worker-handler path.

## Generate framework-neutral modules

Declare separate browser and server consumers:

```ts
import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.example.web",
  entries: {
    apiOrigin: env.public.deployment.origin(),
    applicationOrigin: env.public.deployment.origin(),
    databaseUrl: env.private.deployment.secret(),
  },
  consumers: {
    browser: env.browser(["apiOrigin", "applicationOrigin"]),
    server: env.server(["databaseUrl"]),
  },
  targets: {
    browserDeployment: env.process("browser", {
      apiOrigin: "API_ORIGIN",
      applicationOrigin: "APPLICATION_ORIGIN",
    }),
    serverDeployment: env.process("server", {
      databaseUrl: "DATABASE_URL",
    }),
  },
});
```

Generate and check the application-owned modules:

```sh
pnpm exec astilba-env generate
pnpm exec astilba-env generate --check
```

The browser target creates:

- `.astilba/env/browserDeployment.server.ts`, which checks the application source; and
- `.astilba/env/browser/browser.deployment.ts`, which contains the public projection and decoder without values.

## Keep server and browser modules separate

Treat the generated server target and browser projection as two different import roots. A small application-owned layout makes the boundary visible:

```text
environment/
├── public-env.server.ts       # server target, source checks, response assembly
├── public-env.client.ts       # browser projection and shared readiness promise
└── public-env-provider.tsx    # React distribution and application UI policy
```

Add `import "server-only"` to the server module and `import "client-only"` to the browser module. Do not re-export both through a `public-env` barrel. A Client Component or `instrumentation-client.ts` that reaches a mixed barrel can pull the generated `*.server.ts` target and `@astilba/env/runtime` into Turbopack's browser graph.

The generated `.server.ts` suffix communicates intent, but the application owns this framework boundary. Import server targets directly from server-owned modules; import only `@astilba/env/browser` and generated `browser/*` projections from client-owned modules.

## Load private configuration on the server

Import a generated server target only from server-owned code:

```ts
import "server-only";

import { load } from "../.astilba/env/serverDeployment.server";

export const configuration = load(process.env);
```

Use `check(process.env)` instead when a route or startup boundary needs to choose its own failure response. Never pass the resulting private configuration through props to a Client Component.

## Choose a browser delivery mode

| Mode | Use it when | Cost |
| --- | --- | --- |
| Same-origin JSON route with `loadBrowserBootstrap` | You want to keep the document shell static and can delay configuration-dependent UI. | One no-store configuration request; dependent UI begins asynchronously. |
| Framework-transported JSON with `parseBrowserBootstrap` | Configuration must be available synchronously to the first Client Component render, or the application already renders deployment data on the server. | The server rendering path that produces deployment values must run per request; the envelope participates in the rendered response. |

The route mode is a useful default for a static shell. The transported mode removes the extra request; it does not preserve an ambient `env(key)` or `window.__ENV__` API. In either case, pass the validated typed values through an application-owned provider or props.

## Option 1: load through a same-origin JSON route

Choose this mode when deployment values should vary independently of a static document shell. The route runs on the Node.js runtime; the browser fetches and validates the public envelope after the shell is served.

### Add an App Router endpoint

Return the exact public envelope from a dynamic route:

```ts
// app/api/env/route.ts
import { BOOTSTRAP_PROTOCOL } from "@astilba/env/browser";
import { NextResponse } from "next/server";

import { projection } from "../../../.astilba/env/browser/browser.deployment";
import { check } from "../../../.astilba/env/browserDeployment.server";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export const GET = (): NextResponse => {
  const result = check(process.env);

  if (!result.ok) {
    return NextResponse.json(
      { diagnostics: result.diagnostics, ok: false },
      {
        headers: { "Cache-Control": "private, no-store" },
        status: 500,
      }
    );
  }

  return NextResponse.json(
    {
      audience: { origin: result.value.applicationOrigin },
      consumer: projection.consumer,
      contract: projection.contract,
      lifecycle: projection.lifecycle,
      projection: projection.digest,
      protocol: BOOTSTRAP_PROTOCOL,
      values: result.value,
    },
    { headers: { "Cache-Control": "private, no-store" } }
  );
};
```

`APPLICATION_ORIGIN` must be the trusted canonical origin that serves both the page and endpoint. Do not derive it directly from an untrusted `Host` or forwarded header.

### Add a Pages Router endpoint

The Pages Router uses the same generated modules and envelope. Keep its API handler on the Node.js runtime, not the Edge Runtime:

```ts
// pages/api/env.ts
import { BOOTSTRAP_PROTOCOL } from "@astilba/env/browser";
import type { NextApiRequest, NextApiResponse } from "next";

import { projection } from "../../.astilba/env/browser/browser.deployment";
import { check } from "../../.astilba/env/browserDeployment.server";

export default function handler(
  _request: NextApiRequest,
  response: NextApiResponse
): void {
  const result = check(process.env);

  response.setHeader("Cache-Control", "private, no-store");

  if (!result.ok) {
    response.status(500).json({
      diagnostics: result.diagnostics,
      ok: false,
    });
    return;
  }

  response.status(200).json({
    audience: { origin: result.value.applicationOrigin },
    consumer: projection.consumer,
    contract: projection.contract,
    lifecycle: projection.lifecycle,
    projection: projection.digest,
    protocol: BOOTSTRAP_PROTOCOL,
    values: result.value,
  });
}
```

Both response helpers produce the required JSON content type. The Env browser loader also requests with `cache: "no-store"` and refuses redirects.

### Share one browser readiness promise

Create one client-only module that owns the bootstrap request. Instrumentation, the provider, and other browser consumers can await the same promise without starting duplicate requests:

```ts
// environment/public-env.client.ts
import "client-only";

import {
  loadBrowserBootstrap,
  type ValidatedBootstrap,
} from "@astilba/env/browser";

import {
  type Configuration,
  projection,
} from "../.astilba/env/browser/browser.deployment";

let readiness: Promise<ValidatedBootstrap<Configuration>> | undefined;

export const ensureBrowserEnvironment = (): Promise<
  ValidatedBootstrap<Configuration>
> => {
  readiness ??= loadBrowserBootstrap({
    endpoint: "/api/env",
    expectedAudience: { origin: window.location.origin },
    fetch: globalThis.fetch,
    projection,
    requestBaseUrl: window.location.href,
  });

  return readiness;
};
```

The module caches the in-flight, fulfilled, or rejected validation for the current page. This keeps React development remounts and a separate `instrumentation-client.ts` on one request. With the sample above, a rejected promise remains rejected, and the provider stays in its error state until the page reloads. If your application offers retry without a reload, it must deliberately replace the cached promise with a new validated load rather than weakening the audience or projection checks.

Start configuration-dependent instrumentation from the same promise without blocking module evaluation, and handle rejection explicitly:

```ts
// instrumentation-client.ts
import { ensureBrowserEnvironment } from "./environment/public-env.client";
import { startInstrumentation } from "./instrumentation";

void ensureBrowserEnvironment()
  .then(
    ({ values }) => startInstrumentation(values),
    () => {
      // The provider owns the user-visible configuration failure state.
    }
  )
  .catch(() => {
    // The application owns instrumentation startup failure reporting.
  });
```

Do not use an unhandled top-level `await` or start a second bootstrap request from instrumentation.

### Load before rendering dependent UI

Create a Client Component that owns loading, success, and failure while the client-only module owns readiness:

```tsx
// environment/public-env-provider.tsx
"use client";

import { createContext, useEffect, useState } from "react";

import type { Configuration } from "../.astilba/env/browser/browser.deployment";
import { ensureBrowserEnvironment } from "./public-env.client";

export const EnvironmentContext = createContext<
  Readonly<Configuration> | undefined
>(undefined);

type EnvironmentState =
  | { status: "loading" }
  | { status: "ready"; values: Readonly<Configuration> }
  | { status: "error" };

export function EnvironmentProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const [state, setState] = useState<EnvironmentState>({
    status: "loading",
  });

  useEffect(() => {
    let active = true;

    void ensureBrowserEnvironment().then(
      ({ values }) => {
        if (active) {
          setState({ status: "ready", values });
        }
      },
      () => {
        if (active) {
          setState({ status: "error" });
        }
      }
    );

    return () => {
      active = false;
    };
  }, []);

  if (state.status === "loading") {
    return (
      <p aria-live="polite" role="status">
        Loading configuration.
      </p>
    );
  }

  if (state.status === "error") {
    return <p role="alert">Configuration is unavailable.</p>;
  }

  return (
    <EnvironmentContext.Provider value={state.values}>
      {children}
    </EnvironmentContext.Provider>
  );
}
```

Mount the provider in `app/layout.tsx` or `pages/_app.tsx`. Keep dependent children out of the tree until validation succeeds, and replace the sample status text with application-specific UI. This mode adds one no-store configuration request before dependent UI can render.

### Diagnose a browser import leak

If Turbopack reports that it cannot resolve `@astilba/env/runtime` from a Client Component or `instrumentation-client.ts`, inspect the import path before changing package resolution. The usual cause is a client-safe helper importing a barrel that also exports a server component or generated `*.server.ts` target.

Split the modules, add Next's `server-only` and `client-only` sentinels, and confirm that the browser graph reaches only the generated browser projection and `@astilba/env/browser`. Do not alias the runtime to an empty browser module; that would conceal the boundary violation.

`serverExternalPackages: ["@astilba/env"]` can be an application-specific server-bundling choice after the import graph is clean. It does not repair a server module that is reachable from browser code.

### Keep the static shell static

For public build values, import the generated browser `.build.ts` module directly. When deployment values vary, keep the page static and make only the application-owned `/api/env` route `force-dynamic`; the browser loads and validates its public projection after the shell is served.

## Option 2: transport a serialized envelope through Next.js

Choose this mode when the browser needs validated deployment values synchronously. The server constructs the exact envelope, serializes it with `JSON.stringify`, and passes that inert string through Next.js to a Client Component. `parseBrowserBootstrap` validates the serialized JSON without a fetch.

For deployment values to vary for one built artifact, the server path that creates the envelope must run at request time. In App Router, mark that path dynamic and keep it on the Node.js runtime. This is a trade-off with the static-shell route mode; it is not Next.js Edge Runtime support.

```tsx
// app/layout.tsx
import "server-only";

import { BOOTSTRAP_PROTOCOL } from "@astilba/env/browser";

import { EnvironmentProvider } from "./environment-provider";
import { projection } from "../.astilba/env/browser/browser.deployment";
import { check } from "../.astilba/env/browserDeployment.server";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  const result = check(process.env);

  if (!result.ok) {
    throw new Error("Environment configuration is unavailable.");
  }

  const serverExpectedOrigin = result.value.applicationOrigin;
  const source = JSON.stringify({
    audience: { origin: serverExpectedOrigin },
    consumer: projection.consumer,
    contract: projection.contract,
    lifecycle: projection.lifecycle,
    projection: projection.digest,
    protocol: BOOTSTRAP_PROTOCOL,
    values: result.value,
  });

  return (
    <html lang="en">
      <body>
        <EnvironmentProvider
          serverExpectedOrigin={serverExpectedOrigin}
          source={source}
        >
          {children}
        </EnvironmentProvider>
      </body>
    </html>
  );
}
```

The Client Component receives the serialized source and validates it before distributing the typed values:

```tsx
// app/environment-provider.tsx
"use client";

import { parseBrowserBootstrap } from "@astilba/env/browser";
import { createContext, useMemo } from "react";

import {
  type Configuration,
  projection,
} from "../.astilba/env/browser/browser.deployment";

export const EnvironmentContext = createContext<
  Readonly<Configuration> | undefined
>(undefined);

export function EnvironmentProvider({
  children,
  serverExpectedOrigin,
  source,
}: {
  children: React.ReactNode;
  serverExpectedOrigin: string;
  source: string;
}) {
  const values = useMemo(
    () => {
      const expectedAudience = {
        origin:
          typeof window === "undefined"
            ? serverExpectedOrigin
            : window.location.origin,
      };

      return parseBrowserBootstrap({
        expectedAudience,
        projection,
        source,
      }).values;
    },
    [serverExpectedOrigin, source]
  );

  return (
    <EnvironmentContext.Provider value={values}>
      {children}
    </EnvironmentContext.Provider>
  );
}
```

Pass the serialized string as a component prop or use a framework-supported inert data container. Let Next.js serialize and escape the prop or container contents. Do not use `dangerouslySetInnerHTML`, interpolate JSON into HTML, emit a `beforeInteractive` assignment, or write a mutable `window.__ENV__` global.

`source.audience` remains the trusted canonical server configuration. During server rendering, the Client Component uses `serverExpectedOrigin` because `window` is unavailable. During browser rendering and hydration, it independently expects `window.location.origin`. A page or proxy that serves an envelope for the wrong origin therefore fails parsing during the browser render or hydration; it does not silently trust the envelope's own audience field.

For local HTTP development, select `serverExpectedOrigin` from the same exact development allowlist used to create `source.audience`. The browser still compares it with its current origin. Follow the [local HTTP audience guidance](/docs/env/browser-delivery/#develop-with-a-local-http-origin); do not derive either value from `Host`, `Forwarded`, or `X-Forwarded-Host`.

`parseBrowserBootstrap` throws `BootstrapFailure` when the envelope is missing or invalid. Route that failure to application-specific, perceivable error UI; do not fall back to ambient, baked, or previously cached values. A provider in the root layout needs `app/global-error.tsx` to handle root-layout check or parse errors. A provider mounted by a page can use a nearer `error.tsx` boundary.

In Pages Router, export `getServerSideProps` from every page that needs deployment values; it cannot run in `pages/_app.tsx`. Each page passes the serialized source and `serverExpectedOrigin` through `pageProps` to the provider in `_app.tsx`. Do not use `getStaticProps` for deployment values that must change without rebuilding.

There is no `@astilba/env/next` export. The [Next static shell example](https://github.com/astilbahq/env/tree/main/examples/next-static-shell) is an executable adoption fixture that keeps framework wiring in the application. Its combined target and request-URL audience are local fixture wiring, not the production route pattern. For production, use the separate public target and trusted configured origin shown on this page. Read [Deliver browser configuration](/docs/env/browser-delivery/) for the complete envelope protocol, canonical-origin, cache, and failure requirements. If you are replacing `DynamicEnvScript`, `clientEnv`, or `serverEnv`, continue with [Migrate from next-dynamic-env](/docs/env/migrate-from-next-dynamic-env/).
