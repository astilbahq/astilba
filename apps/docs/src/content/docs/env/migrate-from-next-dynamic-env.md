---
title: Migrate from next-dynamic-env
description: Replace next-dynamic-env proxies and script injection with an explicit Env contract and generated application boundaries.
---

[`next-dynamic-env`](https://github.com/ReesMorris/next-dynamic-env) is retired and deprecated on npm. Astilba Env covers its build-once deployment use case, but does not preserve that package's API or runtime mechanism. The migration replaces ambient proxies and script injection with an explicit contract, generated browser and server modules, and application-owned inert JSON delivery.

Treat this as an architectural migration, not a package rename.

Install the exact public-alpha release before declaring the replacement contract:

```sh
pnpm add @astilba/env@0.3.0 --save-exact
```

## Map the concepts

| `next-dynamic-env` | Astilba Env 0.3 |
| --- | --- |
| `createDynamicEnv({ client, server })` | [`defineEnvironment({ entries, consumers, targets })`](/docs/env/declaration-reference/#defineenvironment) |
| `clientEnv` proxy | Generated browser build configuration or validated bootstrap values |
| `serverEnv` proxy | Generated server target `check` or `load` |
| `DynamicEnvScript` | Application-owned JSON endpoint plus `loadBrowserBootstrap` |
| `waitForEnv` | Await `loadBrowserBootstrap` or use `startBrowserApplication` |
| Inline `window.__NEXT_DYNAMIC_ENV__` value | Inert, same-origin JSON response |
| Validator tuple beside each current value | Lifecycle-aware codec in the declaration |
| Automatic validation skip during `next build` | Explicit build, deployment, and request lifecycles |
| `emptyStringAsUndefined` global option | Per-codec blank, required, and normalisation policy |
| Framework package with one root export | Framework-neutral root, browser, runtime, and Vite boundaries |

There is no `@astilba/env/next` export. App Router and Pages Router integrations use application code around the same generated modules.

## Replace the runtime declaration

A `next-dynamic-env` declaration reads current values while it constructs `clientEnv` and `serverEnv`. An Env declaration describes values without reading them:

```ts
// astilba.env.ts
import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.example.web",
  entries: {
    apiOrigin: env.public.deployment.origin(),
    applicationOrigin: env.public.deployment.origin(),
    databaseUrl: env.private.deployment.secret(),
    port: env.private.deployment.safeInteger({
      maximum: 65_535,
      minimum: 1,
    }),
  },
  consumers: {
    browser: env.browser(["apiOrigin", "applicationOrigin"]),
    server: env.server(["databaseUrl", "port"]),
  },
  targets: {
    browserDeployment: env.process("browser", {
      apiOrigin: "API_ORIGIN",
      applicationOrigin: "APPLICATION_ORIGIN",
    }),
    serverDeployment: env.process("server", {
      databaseUrl: "DATABASE_URL",
      port: "PORT",
    }),
  },
});
```

Public exposure is determined by both the entry visibility and the browser consumer selection. A variable name does not become public merely because it has a `NEXT_PUBLIC_` prefix.

Generate and check the application-owned interfaces:

```sh
pnpm exec astilba-env generate
pnpm exec astilba-env generate --check
```

## Replace `serverEnv`

Replace ambient proxy access with an explicit generated target:

```ts
import "server-only";

import { load } from "./.astilba/env/serverDeployment.server";

const configuration = load(process.env);

configuration.databaseUrl;
configuration.port;
```

Use `check` where application code needs to choose the failure response. Diagnostics contain stable codes and logical identities where appropriate; they do not echo configuration values.

## Replace browser injection

Remove `DynamicEnvScript`, the mutable `clientEnv` proxy, and `waitForEnv`. Add the application-owned Next.js JSON route and Client Component described in [Next.js](/docs/env/nextjs/).

The replacement has three explicit pieces:

1. a generated server target checks the public source values;
2. the route returns the exact public envelope with `Cache-Control: private, no-store`; and
3. `@astilba/env/browser` validates the same-origin response before dependent UI renders.

Use [Deliver browser configuration](/docs/env/browser-delivery/) when you need the complete framework-neutral protocol and failure behavior.

Do not keep both delivery mechanisms active. Once the JSON bootstrap path passes application tests, remove the inline script and every read from `window.__NEXT_DYNAMIC_ENV__`.

## Understand the validation differences

| Previous behavior | Migration decision |
| --- | --- |
| Validation ran while `createDynamicEnv` built its proxies | Env checks a generated target when your code calls `check` or `load`. |
| Validation was automatically skipped during `next build` | Mark values as `build`, `deployment`, or `request`; there is no automatic Next build bypass. |
| Raw values could be accepted without a schema | Choose an explicit [Env codec](/docs/env/declaration-reference/#built-in-codecs). |
| One global option converted empty strings to missing values | Configure blank and required behavior on each codec. |
| Validator transforms supplied defaults or arbitrary output types | Use a built-in codec, a private opaque schema, or application validation after loading. |
| Validation errors could throw, warn, or call a handler | Use `check` for an explicit result or `load` for an exception. |
| Client and server values lived behind runtime proxies | Generated browser and server modules create a static import boundary. |

The browser projection accepts only Env's portable public codecs. Arbitrary schemas and opaque transforms cannot enter a browser consumer.

## Migrate Yup validation deliberately

Prefer built-in codecs for common configuration:

```ts
entries: {
  enabled: env.public.deployment.boolean(),
  mode: env.public.deployment.enum(["standard", "compact"]),
  origin: env.public.deployment.origin(),
  port: env.private.deployment.safeInteger({
    maximum: 65_535,
    minimum: 1,
  }),
}
```

For a genuinely custom private transform, use an `opaque` entry and pass an exactly typed, synchronous Standard Schema v1 implementation to the generated target. A Yup adapter can wrap `validateSync`; it must translate success or failure into the Standard Schema result without exposing the rejected value.

Read [Validation and Standard Schema](/docs/env/validation-and-standard-schema/) for the exact type, runtime, CLI, and portability limits.

If the Yup schema represents application business rules instead of configuration syntax, load a built-in private value and validate it after the Env boundary.

## Account for intentional non-compatibilities

Env does not provide compatibility exports or shims for:

- `createDynamicEnv`;
- `clientEnv` or `serverEnv`;
- `DynamicEnvScript`;
- `waitForEnv`;
- `window.__NEXT_DYNAMIC_ENV__`;
- `__raw`;
- `skipValidation` or automatic build-phase detection;
- `onValidationError`;
- a global `emptyStringAsUndefined` switch;
- implicit `NEXT_PUBLIC_*` exposure; or
- `@astilba/env/next`.

These omissions preserve explicit lifecycles, static artifact boundaries, and inert browser delivery.

## Verify and remove the old package

Before removing `next-dynamic-env`:

1. run `pnpm exec astilba-env generate --check` in CI;
2. fail application startup or the endpoint when a required deployment value is missing;
3. verify browser bundles contain no private entry name, binding name, or value;
4. verify the JSON response has the expected audience and `Cache-Control: private, no-store`;
5. change deployment values without rebuilding and confirm one built artifact observes the new values;
6. exercise successful and rejected bootstrap responses; and
7. remove every import, script component, and global reference from `next-dynamic-env`.

Remove the old dependency only after those checks pass:

```sh
pnpm remove next-dynamic-env
```

The Env package tests exercise App Router and Pages Router builds in static and request modes. Your route, proxy trust, startup policy, and failure UI still require application tests.
