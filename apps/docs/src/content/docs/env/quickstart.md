---
title: Configure a Node application
description: Install Astilba Env, declare deployment configuration, generate typed modules, and fail startup safely.
---

In this guide, you will replace direct `process.env` reads with one typed deployment target. Env will validate the values without logging them and generate a module owned by your application.

## Check the requirements

Env 0.3.0 requires a supported Node.js release:

- Node.js 22.14.0 or later within Node 22;
- Node.js 24.x; or
- Node.js 26.x.

The default `astilba.env.ts` filename requires an ESM package with `"type": "module"`. A lowercase `.mts` configuration file works without changing the package type.

Install the exact public-alpha release:

```sh
pnpm add @astilba/env@0.3.0 --save-exact
```

## Declare the configuration

Create `astilba.env.ts` at the package root:

```ts
import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.example.api",
  entries: {
    apiOrigin: env.public.deployment.origin(),
    databaseUrl: env.private.deployment.secret(),
    port: env.private.deployment.integer({
      minimum: 1,
      maximum: 65_535,
    }),
  },
  consumers: {
    server: env.server(["apiOrigin", "databaseUrl", "port"]),
  },
  targets: {
    serverDeployment: env.process("server", {
      apiOrigin: "API_ORIGIN",
      databaseUrl: "DATABASE_URL",
      port: "PORT",
    }),
  },
});
```

The declaration does not read the three environment variables. It records:

- which logical entries exist;
- whether each entry is public or private;
- when each value becomes available;
- which artifact may consume it; and
- how the target maps logical entries to source names.

`public` means the value may be exposed to a browser consumer. It does not mean that Env publishes the value. `private` entries cannot enter a browser projection.

## Generate the target module

Generate the application-owned output:

```sh
pnpm exec astilba-env generate
```

The target in this guide produces `.astilba/env/serverDeployment.server.ts`. Import its generated `load` function during startup:

```ts
import { load } from "./.astilba/env/serverDeployment.server";

const configuration = load(process.env);

startServer({
  databaseUrl: configuration.databaseUrl,
  origin: configuration.apiOrigin,
  port: configuration.port,
});
```

`load` returns a frozen, typed configuration when every value is valid. It throws `EnvironmentConfigurationError` otherwise. The error carries redacted diagnostics; it does not include the rejected values.

Use `check` when your application needs to choose its own failure response:

```ts
import { check } from "./.astilba/env/serverDeployment.server";

const result = check(process.env);

if (!result.ok) {
  console.error("Configuration is invalid.", result.diagnostics);
  process.exitCode = 1;
} else {
  startServer(result.value);
}
```

The diagnostic entries identify stable error codes and logical entry names. Do not add the original source record to logs.

## Check values without starting the application

The CLI can validate the target against the current process environment:

```sh
API_ORIGIN=https://api.example.com \
DATABASE_URL=postgres://example \
PORT=3000 \
pnpm exec astilba-env check --target serverDeployment
```

A valid target exits with status `0`. Missing or invalid values exit with status `1`. The command reports whether the target is valid, never the values it observed.

## Keep generated output current

Add repeatable scripts:

```json
{
  "scripts": {
    "env:generate": "astilba-env generate",
    "env:check": "astilba-env generate --check"
  }
}
```

Run `pnpm env:generate` after changing the declaration. Commit `astilba.env.ts` and `.astilba/env/`, then require `pnpm env:check` in CI.

`generate --check` performs no writes. It exits nonzero when a generated file is missing, changed, or unexpected. Keeping `snapshot.json` in Git also lets [`astilba-env plan --base`](/docs/env/cli-reference/#plan) compare a proposed contract with a committed revision without executing the historical configuration file.

Next, read [Lifecycles and projections](/docs/env/lifecycles-and-projections/) before adding build, browser, or request configuration.
