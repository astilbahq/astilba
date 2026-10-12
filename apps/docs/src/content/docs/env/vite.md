---
title: Vite
description: Keep private Env declarations, targets, and metadata out of Vite browser graphs.
---

The `@astilba/env/vite` integration rejects private Env surfaces when Vite builds browser code. It is a build-time import boundary; it does not generate configuration, create an endpoint, or load browser values.

Env 0.3.0 supports Vite 8.1.5 or later within Vite 8.

## Add the boundary plugin

Add the plugin to every Vite configuration that can produce a browser graph:

```ts
import { astilbaEnvBrowserBoundary } from "@astilba/env/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [astilbaEnvBrowserBoundary()],
});
```

Keep the plugin active in development and production builds. A development-only boundary can allow an import that fails or leaks later in CI.

## Understand the allowed graph

Browser modules may import:

- `@astilba/env/browser`;
- generated `browser/*.build.ts` values; and
- generated `browser/*.deployment.ts` or `browser/*.request.ts` projections; and
- generated `consumers/*.public.json` evidence.

The plugin rejects imports of:

- the root `@astilba/env` declaration package;
- `@astilba/env/runtime` and `@astilba/env/vite`;
- `astilba.env.ts` or `.mts`;
- generated `*.server.ts` modules;
- `contract.json`, `snapshot.json`, and generated `consumers/*.server.json` metadata; and
- package-owned files outside the public browser runtime.

The plugin emits the stable `ENV_BROWSER_PRIVATE_IMPORT` failure code. Use Vite's import trace to locate the refused edge, then move it behind a server boundary instead of adding an exception.

## Choose build-fixed or deployment-bootstrap values

Use a public `build` entry and its generated browser `.build.ts` module when Vite may fix the value at build time. When one built browser shell must receive deployment values, expose the generated public projection through an application-owned endpoint and bootstrap it in the browser.

The [Vite browser shell example](https://github.com/astilbahq/env/tree/main/examples/vite) is an executable adoption fixture that demonstrates both choices. Its `Host`-derived audience fallback is restricted to exact local `localhost` and loopback forms, and its combined server target deliberately exercises a private secret. For production wiring, use a separate public bootstrap target and a configured canonical HTTPS audience as described in [Deliver browser configuration](/docs/env/browser-delivery/).

## Keep endpoint behavior elsewhere

The Vite plugin does not decide:

- which route returns browser configuration;
- where the canonical audience origin comes from;
- whether a request needs authentication;
- which cache headers the response uses; or
- how the application presents a bootstrap failure.

Use [Deliver browser configuration](/docs/env/browser-delivery/) for the JSON protocol and [Browser](/docs/env/browser-runtime/) for runtime loading.

## Verify the production artifact

The plugin is one layer, not proof that every application import path is safe. In CI:

1. run `astilba-env generate --check`;
2. build every browser entry with the plugin active;
3. scan production assets for private logical names and binding names; and
4. include a non-production canary value and prove it does not enter browser output.

Frameworks or build paths that do not use Vite need an equivalent application-owned rule.
