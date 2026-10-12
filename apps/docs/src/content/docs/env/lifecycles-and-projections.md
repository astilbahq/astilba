---
title: Lifecycles and projections
description: Model when configuration becomes available and keep each application artifact on a physically separate projection.
---

Env separates two decisions that ambient environment access usually combines:

1. the **lifecycle** says when a value may be resolved; and
2. the **consumer projection** says which artifact may know that the entry exists.

The result is one declaration with several generated interfaces, not one mutable configuration object shared across the application.

## Choose the lifecycle

| Lifecycle | Resolve it when | Artifact effect |
| --- | --- | --- |
| `build` | Producing an application artifact | A browser-selected value is emitted into a generated browser `.build.ts` module; a server-only selection resolves through a generated Node.js build target. Changing either requires another build. |
| `deployment` | Starting or configuring one deployment | Application artifact bytes stay unchanged. Server code loads it from an explicit source; browser code receives it through a validated bootstrap. |
| `request` | Handling one request or tenant context | Pass a request-owned source to the generated target. Do not retain the result in process-global state or an unpartitioned cache. |

Only public entries have a build builder. Env does not offer `env.private.build`: embedding a private value in an artifact would make the artifact configuration-specific and copy the value into durable bytes. A browser consumer that selects build entries requires exactly one complete build target so generation has one unambiguous source mapping.

Use deployment values for configuration that should change between environments without rebuilding. Use request values only when the value genuinely differs per request or tenant; the explicit source and lifetime are part of the safety boundary.

## Select a consumer projection

A consumer chooses the exact logical entries one artifact needs:

```ts
consumers: {
  browser: env.browser(["apiOrigin", "featureMode"]),
  worker: env.server(["apiOrigin", "databaseUrl", "tenantId"]),
}
```

`env.browser(...)` accepts public, browser-portable entries only. Its generated projection contains public entry identities, portable decoders, and a compatibility digest. It excludes:

- private entry names and codecs;
- private source bindings;
- server-only codecs;
- co-presence rules and their entries;
- complete contract metadata; and
- configuration values.

`env.server(...)` may select public and private entries. Calling `env.server()` with no list selects every declared entry. Prefer an explicit list when a process has more than one independently deployed artifact.

Calling `env.browser()` with no list also selects every entry, then rejects the declaration if any selection is private, uses a server-only codec, or belongs to a co-presence rule. Co-presence rules are server-projection only in 0.3. An explicit browser list makes the exposure decision easier to review.

## Bind one complete lifecycle per target

A process target maps one consumer and one lifecycle to names in an application-owned source record:

```ts
targets: {
  serverDeployment: env.process("worker", {
    apiOrigin: "API_ORIGIN",
    databaseUrl: "DATABASE_URL",
  }),
  serverRequest: env.process("worker", {
    tenantId: "TENANT_ID",
  }),
}
```

Each target must bind every selected entry for exactly one lifecycle. A target cannot mix deployment and request bindings or omit one selected deployment entry. You can define alternate complete targets for the same consumer and lifecycle when your application needs different source mappings.

The generated module accepts any plain source record, not only `process.env`:

```ts
import { load } from "./.astilba/env/serverRequest.server";

export const handleRequest = (request: Request): Response => {
  const configuration = load({
    TENANT_ID: readTrustedTenant(request),
  });

  return respondForTenant(configuration.tenantId);
};
```

Env validates the supplied record and returns an owned configuration value. Your application remains responsible for authenticating the request, deriving the source, and limiting the value's lifetime.

## Understand the generated separation

Generation writes these kinds of files under `.astilba/env/`:

| Output | Purpose |
| --- | --- |
| `<target>.server.ts` | Typed `check` and `load` functions for one process target. |
| `browser/<consumer>.build.ts` | Frozen public build values for direct browser import. |
| `browser/<consumer>.deployment.ts` | Public deployment projection and decoder; contains no values. |
| `browser/<consumer>.request.ts` | Public request projection and decoder; contains no values. |
| `consumers/<consumer>.*.json` | Value-free public or server projection evidence. |
| `contract.json` | Complete value-free contract evidence. Keep this file out of browser graphs. |
| `snapshot.json` | Value-free planning input used by `plan --base`. |
| `manifest.json` | The exact generated-directory file list and format. |

The generated server and metadata files can contain private logical names and source bindings. Physical separation works only when your build graph imports browser modules deliberately. Add the [Vite boundary plugin](/docs/env/vite/) where Vite builds browser code.

## Treat compatibility as exact or unknown

Env derives projection digests from the declared contract, not the current values. A matching digest proves that the consumer sees the same declared projection.

Opaque validators are different: their `semantics` and `revision` fields describe compatibility, but Env cannot prove that two arbitrary implementations behave the same. Planning therefore reports unknown confidence where a safe conclusion is unavailable.

This is compatibility evidence, not secret management. Env never provisions a value source, rotates a secret, or verifies that a live provider contains the declared value.
