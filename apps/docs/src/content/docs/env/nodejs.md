---
title: Node.js
description: Author Env declarations, generate project-owned modules, and resolve typed configuration in supported Node.js releases.
---

Node.js is Env's authoring and tooling runtime. Use it to execute `astilba.env.ts`, generate project-owned modules, check generated drift, validate process targets through the CLI, and plan declaration changes.

Generated server targets also run in Node.js. They read only the source object you pass to `check` or `load`; Env does not add a global environment loader.

## Check the supported releases

Env 0.3.0 supports:

- Node.js 22.14.0 or later within Node 22;
- Node.js 24 within Node 24; and
- Node.js 26 within Node 26.

Use an ESM package with `"type": "module"` for the default `astilba.env.ts` configuration filename. A lowercase `.mts` configuration file is also supported.

## Keep authoring separate from resolution

The declaration describes configuration without reading current values:

```ts
import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.example.api",
  entries: {
    databaseUrl: env.private.deployment.secret(),
    port: env.private.deployment.safeInteger({
      maximum: 65_535,
      minimum: 1,
    }),
  },
  consumers: {
    server: env.server(["databaseUrl", "port"]),
  },
  targets: {
    serverDeployment: env.process("server", {
      databaseUrl: "DATABASE_URL",
      port: "PORT",
    }),
  },
});
```

Run generation on a supported Node.js release:

```sh
pnpm exec astilba-env generate
```

The generated `.astilba/env/serverDeployment.server.ts` module imports the narrow `@astilba/env/runtime` surface. Your application chooses when to pass `process.env`.

## Choose `check` or `load`

Use `check` when application code owns the failure path:

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

Use `load` when invalid configuration should throw:

```ts
import { load } from "./.astilba/env/serverDeployment.server";

const configuration = load(process.env);

startServer(configuration);
```

Both operations return owned, frozen values. Diagnostics contain stable codes and logical identities where appropriate; they do not contain rejected values, fragments, lengths, or hashes.

## Audit the runtime export

Generated server targets import `@astilba/env/runtime` for you. Application code should normally import the generated target's typed `check` or `load` function rather than construct a target definition itself.

The runtime export is public so generated modules can use one stable boundary and advanced consumers can audit every dependency:

| Export | Purpose |
| --- | --- |
| `checkProcessTarget` | Validate and resolve a generated target with built-in codecs without throwing for configuration failures. |
| `loadProcessTarget` | Resolve a generated target with built-in codecs or throw `EnvironmentConfigurationError`. |
| `checkProcessTargetWithSchemas` | Resolve a Node.js target with application-owned Standard Schema validators and return a Promise of the result. |
| `loadProcessTargetWithSchemas` | Resolve a Node.js target with Standard Schema validators or reject with `EnvironmentConfigurationError`. |
| `EnvironmentConfigurationError` | Identify a failed `load*` operation and expose its value-free `diagnostics`. Direct construction is rejected. |
| `ProcessSource` | Describe the application-owned object from which a generated target reads raw values. |
| `ProcessTargetDefinition` | Describe the generated target definition consumed by the runtime. |
| `ProcessTargetSchemas` | Map an opaque entry name to its application-owned Standard Schema validator. |
| `StandardSchemaV1` | Describe the minimal Standard Schema v1 contract accepted for private opaque entries. |
| `StandardSchemaResult` | Describe the immediate success or failure result returned by that validator contract. |

The `*WithSchemas` operations return Promises, but validators must settle synchronously. A Promise or thenable from a validator produces `ENV_VALIDATOR_ASYNC_UNSUPPORTED`. The workerd conditional export does not execute opaque validators and admits deployment targets only.

## Resolve request values explicitly

Node.js targets may also resolve request-lifecycle values from an application-owned object:

```ts
import { load } from "./.astilba/env/serverRequest.server";

export const handleRequest = (request: Request): Response => {
  const configuration = load({
    TENANT_ID: readTrustedTenant(request),
  });

  return respondForTenant(configuration.tenantId);
};
```

Do not retain request configuration in process-global state or an unpartitioned cache. Env validates the supplied object; your application still authenticates the request and owns the source lifetime.

## Keep the CLI in Node.js

The declaration builders, generator, and CLI are Node.js tools even when a generated target runs somewhere else. For example, author and generate a [Cloudflare Workers target](/docs/env/cloudflare-workers/) in Node.js, then import only its generated module in the Worker.

Read [Configure a Node application](/docs/env/quickstart/) for the complete first setup and [CLI reference](/docs/env/cli-reference/) for command behavior.
