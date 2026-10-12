---
title: Overview
description: Understand Astilba Env's contract model, generated boundaries, supported runtimes, and responsibility boundary.
---

Astilba Env turns one portable TypeScript declaration into generated configuration interfaces for the artifacts that consume them. It keeps browser and server projections physically separate, distinguishes build, deployment, and request values, and validates values at the lifecycle where they become available.

The 0.3 release is a public alpha. Use it when explicit configuration boundaries are worth adopting before the API reaches stability.

## Decide whether Env fits

| You want to… | Fit |
| --- | --- |
| Promote one built artifact through several deployments | Declare deployment values and resolve them from each deployment's source object. |
| Keep private names and bindings out of browser bundles | Browser consumers receive generated public projections only. |
| Validate build, deployment, and request values separately | Each entry declares its lifecycle; each generated target resolves one lifecycle. |
| Run without an Astilba service | Generation, checking, and planning are local; the application owns value storage and delivery. |
| Compare declared names with an application-owned provider list | Export a value-free target inventory, convert the provider's names to Env's strict observed format, and check open or closed ownership without giving Env provider credentials. |
| Validate Worker bindings without a Node.js compatibility layer | Env admits a narrow generated deployment-target path for Cloudflare Workers. |
| Inject arbitrary configuration through inline JavaScript | Not supported. Browser deployment and request values use inert, same-origin JSON. |
| Import one mutable environment object everywhere | Not supported. Generated modules create explicit artifact boundaries. |
| Provision secrets or platform bindings | Not supported. Env validates application-owned sources; it does not operate providers. |

## Model four decisions

An Env declaration records four independent decisions:

1. an entry's visibility is `public` or `private`;
2. its lifecycle is `build`, `deployment`, or `request`;
3. its codec defines the accepted input and typed output; and
4. a consumer selects which entries one artifact may know.

A target then maps one consumer and one complete lifecycle to names in an application-owned source object.

```ts
import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.example.application",
  entries: {
    apiOrigin: env.public.deployment.origin(),
    databaseUrl: env.private.deployment.secret(),
  },
  consumers: {
    browser: env.browser(["apiOrigin"]),
    server: env.server(["databaseUrl"]),
  },
  targets: {
    browserDeployment: env.process("browser", {
      apiOrigin: "API_ORIGIN",
    }),
    serverDeployment: env.process("server", {
      databaseUrl: "DATABASE_URL",
    }),
  },
});
```

The declaration does not read either value.

## Generate artifact-specific modules

Run generation on a supported Node.js release and check drift in CI:

```sh
pnpm exec astilba-env generate
pnpm exec astilba-env generate --check
```

The declaration above produces separate interfaces:

```text
.astilba/env/browser/browser.deployment.ts
.astilba/env/browserDeployment.server.ts
.astilba/env/serverDeployment.server.ts
```

Generated target modules export typed `check(source)` and `load(source)` functions. Generated browser build modules may contain frozen public build values; deployment and request projection modules contain the public decoder and compatibility identity for one consumer and lifecycle, but no values, private entry names, private bindings, or complete contract metadata.

Read [Lifecycles and projections](/docs/env/lifecycles-and-projections/) for the generated file model.

## Choose the runtime path

| Path | Lifecycle and generated import | Example |
| --- | --- | --- |
| Node server | Generate on Node.js, then load a generated server target for the lifecycle your server owns. | [Node server](https://github.com/astilbahq/env/tree/main/examples/node-service) |
| Cloudflare Worker deployment target | Run a generated deployment-lifecycle target through `@astilba/env/runtime` with built-in codecs. Authoring and generation stay on Node.js. | [Cloudflare Worker](https://github.com/astilbahq/env/tree/main/examples/cloudflare-worker) |
| Next static shell | Import public build values from the generated browser `.build.ts` module. Bootstrap deployment values through an application-owned route without making the page dynamic. | [Next static shell](https://github.com/astilbahq/env/tree/main/examples/next-static-shell) |
| Vite browser shell | Import public build values from the generated browser `.build.ts` module, or bootstrap deployment values from an application-owned endpoint and generated browser projection. | [Vite browser shell](https://github.com/astilbahq/env/tree/main/examples/vite) |

The Worker path is limited to generated deployment-lifecycle targets with built-in codecs. It does not establish support for declaration authoring, generation, request targets, opaque schemas, or other package exports in Workers.

Framework pages explain the application-owned wiring: [Vite](/docs/env/vite/) adds a private-module browser-graph boundary, [Next.js](/docs/env/nextjs/) keeps static shells static, and [Deliver browser configuration](/docs/env/browser-delivery/) defines both same-origin endpoint delivery and framework-transported inert JSON.

Check [Release and support](/docs/env/release-and-support/) before choosing a runtime. Evidence for one package export does not make every Env export portable to that runtime.

## Compose Env with your stack

Env can replace application-specific configuration parsing, required-value helpers, public and private naming conventions, and projection glue. It complements the systems that store, deliver, inventory, or describe the source values.

| Concern | Existing authority | Env's role |
| --- | --- | --- |
| Secret storage and injection | A secret manager, CI system, or deployment platform owns the values and delivers them to the application. | A generated target reads only the selected values inside your application process after delivery. Astilba receives no values; Env provides no secret storage, rotation, or provisioning. |
| Worker binding types | `wrangler types` describes the complete Worker binding interface. | A generated deployment target decodes mapped string settings and secrets. D1, KV, R2, and service bindings are capabilities, so they stay outside the configuration result and remain available through the Wrangler-generated interface. |
| Live deployment inventory and preflight | Deployment tooling or application-owned assertions query the platform and decide whether a deployment may proceed. | Env validates the explicit source object at the declared lifecycle. It does not query a provider or prove what is present in a remote deployment. |
| Declared-name drift | Application-owned tooling converts a provider or platform name list into Env's strict observed format. | `inventory export` publishes the declared name contract; `inventory check` compares names and required presence. Env never receives values or provider credentials. |
| Server configuration parsing | Hand-written `requiredEnv()` helpers or schema wrappers turn ambient strings into application values. | Generated `check(source)` and `load(source)` operations can replace that parsing while sharing one contract across selected artifacts. |
| Browser configuration delivery | An application-owned route serves inert, same-origin JSON, or a framework safely transports the serialized envelope as inert data. The application owns the HTTP and rendering policy. | Env generates the public projection, compatibility identity, and envelope validation; it does not host a route or choose the transport. |

Use Env when one contract replaces repeated parsing or crosses artifact and lifecycle boundaries. Keep your existing parser when one server already has a reliable parser, you have no browser configuration surface, and you do not need to promote the same artifact through several deployments.

Continue with [Configure a Node application](/docs/env/quickstart/) for the smallest working setup, or [Check name inventory drift](/docs/env/inventory-and-drift/) when duplicated name lists are the problem.
