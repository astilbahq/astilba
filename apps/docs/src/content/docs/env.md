---
title: Env
description: Compile one portable configuration declaration into separated, typed browser and server interfaces.
tableOfContents: false
prev: false
next: false
---

Astilba Env is a local-first configuration contract compiler for TypeScript. You declare which values each application artifact needs, when those values may be resolved, and where they may be exposed. Env generates typed browser and server modules from that contract.

`@astilba/env` 0.3.0 is a public alpha. It is intended for evaluation and controlled adoption while the contract is still free to improve.

```sh
pnpm add @astilba/env@0.3.0 --save-exact
```

Env does not replace your secret manager or `.env` files, and it has no hosted control plane. Generation and checking read only the source values their current operation needs, inside your project. Public build values selected by a browser consumer are deliberately emitted into generated `.build.ts` modules; public build values selected only by a server consumer resolve through a generated server target. Other generated evidence is value-free. Env does not send your values to Astilba.

## Choose your next step

| Goal | Start here |
| --- | --- |
| Decide whether Env fits | [Overview](/docs/env/overview/) |
| Add Env to a Node application | [Configure a Node application](/docs/env/quickstart/) |
| Compare declared names with a provider or platform list | [Check name inventory drift](/docs/env/inventory-and-drift/) |
| Check a runtime boundary | [Node.js](/docs/env/nodejs/) or [Browser](/docs/env/browser-runtime/) |
| Configure a Cloudflare Worker | [Cloudflare Workers](/docs/env/cloudflare-workers/) |
| Integrate a framework | [Vite](/docs/env/vite/) or [Next.js](/docs/env/nextjs/) |
| Choose a runtime path | [Node server, Worker deployment target, Next static shell, or Vite browser shell](/docs/env/overview/#choose-the-runtime-path) |
| Understand build, deployment, and request values | [Lifecycles and projections](/docs/env/lifecycles-and-projections/) |
| Choose built-in or custom validation | [Validation and Standard Schema](/docs/env/validation-and-standard-schema/) |
| Serve validated browser configuration | [Deliver browser configuration](/docs/env/browser-delivery/) |
| Replace `next-dynamic-env` | [Migrate from next-dynamic-env](/docs/env/migrate-from-next-dynamic-env/) |
| Look up builders and codecs | [Declaration reference](/docs/env/declaration-reference/) |
| Automate generation, checking, and planning | [CLI reference](/docs/env/cli-reference/) |
| Check runtimes, exports, and alpha boundaries | [Release and support](/docs/env/release-and-support/) |

Start with the Node quickstart for a new integration. Choose a runtime or framework page when the declaration already exists. Use the migration guide if your application currently exposes runtime configuration through `DynamicEnvScript`, `clientEnv`, or `serverEnv`.
