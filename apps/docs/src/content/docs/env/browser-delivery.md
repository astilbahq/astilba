---
title: Deliver browser configuration
description: Generate a public projection, deliver an inert same-origin JSON envelope, and validate it before browser application startup.
---

Env accepts browser deployment and request configuration as inert JSON. Your application either owns a same-origin endpoint or transports safely escaped serialized JSON through its framework. The generated projection and browser runtime validate that the envelope belongs to the expected contract, consumer, lifecycle, and origin before application code uses it.

Endpoint delivery adds one configuration request. Framework-transported delivery adds no separate request, but the envelope participates in the rendered response. Use a public build entry instead when a value may be fixed in the browser artifact.

## Declare a browser target

Select only the public entries the browser needs:

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

Generate the modules:

```sh
pnpm exec astilba-env generate
```

The browser target produces two different interfaces:

- `.astilba/env/browserDeployment.server.ts` checks the application-owned source; and
- `.astilba/env/browser/browser.deployment.ts` contains the public projection and typed decoder, but no values.

The generated public projection does not contain `databaseUrl`, its source name, or its codec metadata.

## Return the exact envelope

Create a same-origin route that checks the public target and returns the selected values:

```ts
import { BOOTSTRAP_PROTOCOL } from "@astilba/env/browser";

import { projection } from "./.astilba/env/browser/browser.deployment";
import { check } from "./.astilba/env/browserDeployment.server";

export const environmentResponse = (): Response => {
  const result = check(process.env);

  if (!result.ok) {
    return Response.json(
      { diagnostics: result.diagnostics, ok: false },
      {
        headers: { "Cache-Control": "private, no-store" },
        status: 500,
      }
    );
  }

  return Response.json(
    {
      audience: { origin: result.value.applicationOrigin },
      consumer: projection.consumer,
      contract: projection.contract,
      lifecycle: projection.lifecycle,
      projection: projection.digest,
      protocol: BOOTSTRAP_PROTOCOL,
      values: result.value,
    },
    {
      headers: { "Cache-Control": "private, no-store" },
    }
  );
};
```

The successful envelope has exactly seven top-level fields:

| Field | Source |
| --- | --- |
| `protocol` | `BOOTSTRAP_PROTOCOL`, currently `astilba.env.bootstrap/v1`. |
| `contract` | Generated `projection.contract`. |
| `consumer` | Generated `projection.consumer`. |
| `lifecycle` | Generated `projection.lifecycle`. |
| `projection` | Generated `projection.digest`. |
| `audience` | `{ origin }` derived from trusted canonical application configuration. |
| `values` | The successful generated target value. |

Do not derive the audience from an untrusted `Host` or forwarded header. If your platform constructs canonical origins at a trusted proxy boundary, test that boundary as application code.

Return `application/json` with a 2xx status. Redirects are refused. A response that can vary by request must include `Cache-Control: private, no-store`; the loader also requests every bootstrap with `cache: "no-store"`.

The endpoint contains public values, but authentication and authorisation can still matter for request-specific configuration. Env does not choose the route or access policy.

## Validate before use

Load the envelope with the generated projection:

```ts
import { loadBrowserBootstrap } from "@astilba/env/browser";

import { projection } from "./.astilba/env/browser/browser.deployment";

const bootstrap = await loadBrowserBootstrap({
  endpoint: "/api/env",
  expectedAudience: { origin: window.location.origin },
  fetch: globalThis.fetch,
  projection,
  requestBaseUrl: window.location.href,
});

renderApplication(bootstrap.values);
```

The result is typed from the generated projection. Its values are copied into frozen, owned data before return.

`loadBrowserBootstrap` refuses:

- a request URL outside the expected origin;
- fetch failure, redirects, a non-2xx status, or a final cross-origin URL;
- a non-JSON content type, invalid UTF-8, invalid JSON, or a body over 65,536 bytes;
- missing or unknown envelope fields;
- a different audience, protocol, contract, consumer, lifecycle, or projection digest; and
- missing, unknown, or invalid typed values.

It throws `BootstrapFailure` with a stable `code` such as `BOOTSTRAP_PROJECTION_MISMATCH`. Do not continue with ambient, baked, or previously cached values after a failure.

```ts
import {
  BootstrapFailure,
  loadBrowserBootstrap,
} from "@astilba/env/browser";

try {
  const bootstrap = await loadBrowserBootstrap(options);
  renderApplication(bootstrap.values);
} catch (error) {
  const code =
    error instanceof BootstrapFailure ? error.code : "BOOTSTRAP_UNEXPECTED";

  renderConfigurationFailure(code);
}
```

Keep failure UI application-specific and perceivable. A retry should perform a new validated load; it should not weaken any expected identity.

## Delay application import

Use `startBrowserApplication` when configuration must validate before the main application module enters the browser graph:

```ts
import { startBrowserApplication } from "@astilba/env/browser";

import { projection } from "./.astilba/env/browser/browser.deployment";

await startBrowserApplication({
  endpoint: "/api/env",
  expectedAudience: { origin: window.location.origin },
  fetch: globalThis.fetch,
  importApplication: () => import("./application"),
  projection,
  requestBaseUrl: window.location.href,
});
```

The imported module must export `start(values, audience)`. Env loads and validates the bootstrap first, imports the application second, then calls `start`.

If your framework has already transported the exact envelope as inert data, use `parseBrowserBootstrap({ source, expectedAudience, projection })`. It applies the same envelope and value checks without fetching. Pass the serialized JSON text as `source`, not the result of `JSON.parse`, and let the framework escape it for its inert data container. Do not interpolate unescaped JSON into HTML or turn the envelope into executable JavaScript.

## Develop with a local HTTP origin

The Env `origin()` codec accepts canonical HTTPS origins and deliberately rejects `localhost` and IP literals. The browser runtime can still validate an exact HTTP `expectedAudience` during local development.

Use local HTTPS, or derive a development-only audience in application code from an allowlist of exact local origins such as `http://localhost:<port>` and `http://127.0.0.1:<port>`. The endpoint must emit that same allowlisted origin in `audience.origin` when the browser uses it as `expectedAudience`; do not override only the client-side expectation. Keep this branch out of production, reject forwarded host headers, and never weaken the production canonical-origin check. Env does not add a development fallback for you.

For example, select one fixed origin for the current development command, then use the selected value in the endpoint envelope:

```ts
const developmentOrigins = Object.freeze({
  localhost: "http://localhost:3000",
  loopback: "http://127.0.0.1:3000",
});

const audienceOrigin =
  process.env.NODE_ENV === "development"
    ? developmentOrigins.localhost
    : result.value.applicationOrigin;

return Response.json(
  {
    audience: { origin: audienceOrigin },
    consumer: projection.consumer,
    contract: projection.contract,
    lifecycle: projection.lifecycle,
    projection: projection.digest,
    protocol: BOOTSTRAP_PROTOCOL,
    values: result.value,
  },
  {
    headers: { "Cache-Control": "private, no-store" },
  }
);
```

Choose `localhost` or `loopback` explicitly in application-owned development configuration. Do not select between them from `Host`, `Forwarded`, or `X-Forwarded-Host`.

## Use build values without a request

A public build entry is validated during generation and emitted into a browser-only module:

```ts
entries: {
  releaseSha: env.public.build.string({
    minimumCodePoints: 7,
    maximumCodePoints: 64,
  }),
},
consumers: {
  browser: env.browser(["releaseSha"]),
},
targets: {
  browserBuild: env.process("browser", {
    releaseSha: "RELEASE_SHA",
  }),
},
```

Generate with the value available:

```sh
RELEASE_SHA=abcdef0 pnpm exec astilba-env generate
```

Then import the frozen value directly:

```ts
import { configuration } from "./.astilba/env/browser/browser.build";

configuration.releaseSha;
```

Changing `RELEASE_SHA` requires generation and a new application build. The generated module contains the public value, so do not use build entries for secrets.

Keep the browser runtime and generated public modules physically separate from private targets. If Vite builds the browser graph, add the dedicated [Vite boundary](/docs/env/vite/).
