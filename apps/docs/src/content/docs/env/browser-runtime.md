---
title: Browser
description: Load and validate public browser configuration through Env's isolated browser runtime.
---

The `@astilba/env/browser` export validates public configuration before browser application code uses it. It accepts only generated public projections; declaration builders, private targets, provider bindings, and complete contract metadata stay outside the browser graph.

Browser delivery is application-owned. Env validates either a same-origin JSON response or inert JSON that your framework has already transported.

The browser export contains the runtime values below:

| Export | Purpose |
| --- | --- |
| `loadBrowserBootstrap` | Fetch and validate a same-origin JSON envelope. |
| `parseBrowserBootstrap` | Validate an already transported serialized JSON envelope without fetching. |
| `startBrowserApplication` | Validate with `loadBrowserBootstrap`, then import and start the application module. |
| `BootstrapFailure` | Identify an expected bootstrap failure with `instanceof` and read its stable `code`. |
| `BOOTSTRAP_PROTOCOL` | Build an application-owned response with the current `astilba.env.bootstrap/v1` protocol identity. |
| `MAXIMUM_BOOTSTRAP_BYTES` | Read the current 65,536-byte response limit without duplicating it in application code. |

It also exports the supporting `BootstrapFailureCode`, `BrowserApplicationModule`, `BrowserAudience`, `BrowserProjection`, `BrowserValues`, `LoadBootstrapOptions`, `ParseBootstrapOptions`, `StartBrowserApplicationOptions`, and `ValidatedBootstrap` types. Generated projections provide `BrowserProjection`; application code should import a generated projection rather than construct one.

## Import only the browser surface

Generate a public browser consumer and import its projection:

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

`loadBrowserBootstrap` requests with `cache: "no-store"` and `redirect: "error"`. It checks the same-origin request and final response, JSON content type and size, envelope identity, expected audience, generated projection digest, exact fields, and typed values.

The returned values are copied into frozen, owned data.

## Parse framework-transported JSON

Use `parseBrowserBootstrap` when a framework has already delivered the exact envelope as inert data:

```ts
import {
  BootstrapFailure,
  parseBrowserBootstrap,
} from "@astilba/env/browser";

import { projection } from "./.astilba/env/browser/browser.deployment";

const source = document.querySelector("#env")?.textContent ?? "";

try {
  const bootstrap = parseBrowserBootstrap({
    expectedAudience: { origin: window.location.origin },
    projection,
    source,
  });

  renderApplication(bootstrap.values);
} catch (error) {
  const code =
    error instanceof BootstrapFailure ? error.code : "BOOTSTRAP_UNEXPECTED";

  renderConfigurationFailure(code);
}
```

Pass the serialized JSON text, not the result of `JSON.parse`. Let the framework serialize and escape the envelope for an inert data container; do not interpolate unescaped JSON into HTML yourself. Do not replace the data with an executable script assignment or a mutable global. Missing or malformed transported JSON follows the same configuration-failure boundary as an invalid bootstrap.

## Delay the application import

Use `startBrowserApplication` when the main application module must not enter the active graph before configuration validates:

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

The imported module exports `start(values, audience)`. Env validates first, imports second, and calls `start` last.

## Handle failures without fallback

Browser operations throw `BootstrapFailure` with a stable code:

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

Do not continue with ambient, baked, or previously cached values after validation fails. A retry should perform a new validation without weakening the expected audience or projection identity.

The public `BootstrapFailureCode` union is grouped by the boundary that refused the bootstrap:

- request and response: `BOOTSTRAP_REQUEST_ORIGIN_MISMATCH`, `BOOTSTRAP_FETCH_FAILED`, `BOOTSTRAP_REDIRECTED`, `BOOTSTRAP_FINAL_ORIGIN_MISMATCH`, `BOOTSTRAP_HTTP_STATUS_INVALID`, `BOOTSTRAP_INVALID_MIME`, `BOOTSTRAP_BODY_READ_FAILED`, `BOOTSTRAP_BODY_TOO_LARGE`, and `BOOTSTRAP_INVALID_UTF8`;
- JSON input: `BOOTSTRAP_INVALID_JSON`, `BOOTSTRAP_DUPLICATE_KEY`, `BOOTSTRAP_JSON_TOO_DEEP`, `BOOTSTRAP_JSON_TOO_MANY_KEYS`, and `BOOTSTRAP_NON_PORTABLE_JSON`;
- envelope identity: `BOOTSTRAP_UNKNOWN_FIELD`, `BOOTSTRAP_FIELD_MISSING`, `BOOTSTRAP_FIELD_INVALID`, `BOOTSTRAP_PROTOCOL_UNSUPPORTED`, `BOOTSTRAP_CONTRACT_MISMATCH`, `BOOTSTRAP_LIFECYCLE_MISMATCH`, `BOOTSTRAP_PROJECTION_MISMATCH`, and `BOOTSTRAP_AUDIENCE_MISMATCH`; and
- generated projection and values: `BOOTSTRAP_PROJECTION_INVALID`, `BOOTSTRAP_GENERATED_FORMAT_UNSUPPORTED`, `BOOTSTRAP_VALUE_MISSING`, and `BOOTSTRAP_VALUE_INVALID`.

Treat the codes as diagnostics and telemetry identities, not user-facing copy. An unexpected non-Env exception has no Env failure code; map it to an application-owned fallback state.

## Keep private modules out of the graph

Browser code may import:

- `@astilba/env/browser`;
- generated `browser/*.build.ts` modules; and
- generated `browser/*.deployment.ts` or `browser/*.request.ts` projections; and
- generated `consumers/*.public.json` evidence when an application explicitly needs the value-free public manifest.

It must not import the root package, `@astilba/env/runtime`, the Env configuration file, generated `*.server.ts` targets, or complete generated metadata.

Use the [Vite integration](/docs/env/vite/) where Vite builds the browser graph. Other build tools need an equivalent application-owned boundary.

Continue with [Deliver browser configuration](/docs/env/browser-delivery/) to build the endpoint and exact envelope.
