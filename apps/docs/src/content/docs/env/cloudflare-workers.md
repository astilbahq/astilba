---
title: Cloudflare Workers
description: Generate a deployment target on Node.js and validate Wrangler bindings directly inside a Cloudflare Worker.
---

Env 0.3.0 admits a narrow Cloudflare Workers runtime path: a generated server target for the `deployment` lifecycle may call `check(env)` or `load(env)` inside a Worker handler when every selected entry uses a first-party Env codec.

Authoring, generation, CLI checks, and planning still run on a [supported Node.js release](/docs/env/nodejs/). The Worker imports the generated target and the `@astilba/env/runtime` dependency behind it; it does not import the root package, browser runtime, CLI, or Vite integration.

## Declare one deployment target

Declare Worker configuration on Node.js:

```ts
import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.example.worker",
  entries: {
    apiOrigin: env.public.deployment.origin(),
    signingKey: env.private.deployment.secret(),
  },
  consumers: {
    worker: env.server(["apiOrigin", "signingKey"]),
  },
  targets: {
    workerDeployment: env.process("worker", {
      apiOrigin: "API_ORIGIN",
      signingKey: "SIGNING_KEY",
    }),
  },
});
```

Generate and commit the application-owned output:

```sh
pnpm exec astilba-env generate
pnpm exec astilba-env generate --check
```

This creates `.astilba/env/workerDeployment.server.ts`. Generation does not read Worker bindings for a deployment target and does not contact Cloudflare.

## Declare Wrangler bindings

Keep non-secret values in `vars`, and declare required secret names with `secrets.required`:

```jsonc
{
  "$schema": "node_modules/wrangler/config-schema.json",
  "name": "example-worker-staging",
  "main": "src/index.ts",
  "compatibility_date": "2026-07-29",
  "vars": {
    "API_ORIGIN": "https://staging-api.example.com"
  },
  "secrets": {
    "required": ["SIGNING_KEY"]
  },
  "kv_namespaces": [
    {
      "binding": "CACHE",
      "id": "<your-staging-cache-kv-namespace-id>"
    }
  ]
}
```

Configure the secret value through Cloudflare, outside source control. `secrets.required` records the required name; it does not contain the secret.

The same built Worker artifact can use another binding set:

```jsonc
{
  "$schema": "node_modules/wrangler/config-schema.json",
  "name": "example-worker-production",
  "main": "src/index.ts",
  "compatibility_date": "2026-07-29",
  "vars": {
    "API_ORIGIN": "https://api.example.com"
  },
  "secrets": {
    "required": ["SIGNING_KEY"]
  },
  "kv_namespaces": [
    {
      "binding": "CACHE",
      "id": "<your-production-cache-kv-namespace-id>"
    }
  ]
}
```

Env validates values at runtime. Its provider-neutral inventory command can compare the declared string names with an application-supplied name list; it does not call Cloudflare, inspect binding kinds, or decide when a changed set should redeploy.

Choose the latest compatibility date supported by your installed Wrangler release. Env does not impose its own date floor. The [0.3.0 release matrix](https://github.com/astilbahq/env/actions/runs/31482997555) exercises the exact packed archive with stock Wrangler 4.115.0 and its bundled workerd at compatibility date `2026-07-29`; it does not replace Wrangler's transitive runtime.

## Generate Cloudflare's binding types

Generate the Worker `Env` interface from Wrangler configuration:

```sh
pnpm exec wrangler types
pnpm exec wrangler types --check
```

[`wrangler types`](https://developers.cloudflare.com/workers/languages/typescript/#generate-types) derives binding types from your configuration. Use `--check` in CI, so the generated interface cannot drift.

The generated target accepts that interface without requiring a string index signature:

```ts
import { check } from "../.astilba/env/workerDeployment.server";

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const result = check(env);

    if (!result.ok) {
      return Response.json(
        { diagnostics: result.diagnostics, ok: false },
        { status: 500 }
      );
    }

    const cached = await env.CACHE.get(request.url);

    return Response.json({
      apiOrigin: result.value.apiOrigin,
      cached: cached !== null,
    });
  },
} satisfies ExportedHandler<Env>;
```

`check(env)` reads only `API_ORIGIN` and `SIGNING_KEY`, then returns an exact owned result. The unrelated `CACHE` capability binding remains available to application code and does not enter the configuration result.

Use `load(env)` instead when invalid deployment configuration should throw. Do not log the original `env` object on failure.

## Pass string bindings without coercion

Every source binding selected by the generated target must be a string or `undefined`. A missing optional binding may be `undefined`; a present binding is decoded from its exact string value.

Cloudflare also allows JSON values in `vars` and objects for capability bindings. Env does not stringify or coerce those values. If a selected source name resolves to a JSON value, KV namespace, D1 database, service binding, or another capability object, `check` returns a redacted invalid-value diagnostic and `load` throws.

Unselected bindings are different: Env does not read or reject them. Keep `CACHE` and other capabilities outside the target mapping, then use them directly through the Wrangler-generated `Env` interface.

## Keep the compatibility surface narrow

Env's generated runtime does not need the [`nodejs_compat` compatibility flag](https://developers.cloudflare.com/workers/runtime-apis/nodejs/) for this path. Add that flag only when other application dependencies require Node.js APIs.

The Env 0.3.0 Workers claim includes:

- generated server targets for the `deployment` lifecycle;
- first-party Env codecs; and
- direct `check(env)` or `load(env)` calls inside the handler.

It does not include:

- request-lifecycle generated targets;
- `opaque` entries or caller-provided Standard Schema validators;
- the root `@astilba/env` authoring export in workerd;
- `@astilba/env/browser` or `@astilba/env/vite` in workerd; or
- the Env CLI in workerd.

## Know what Env does not operate

Env does not provide:

- Worker, route, or binding provisioning;
- secret storage or rotation;
- a live Cloudflare binding query or Wrangler-specific parser;
- a Cloudflare provider API client; or
- automatic deployment or redeployment planning.

Wrangler and Cloudflare own those operations. If duplicated string-binding names are the problem, convert a Wrangler-produced name list into Env's provider-neutral observed format and follow [Check name inventory drift](/docs/env/inventory-and-drift/). See the official [Wrangler configuration reference](https://developers.cloudflare.com/workers/wrangler/configuration/) for binding configuration and required-secret behavior.
