---
title: Release and support
description: Check the package, CLI, runtime, framework, and public-alpha boundaries of Astilba Env 0.3.0.
---

`@astilba/env` 0.3.0 is a public alpha. The package and source are public, so applications can evaluate the complete contract, but the API may change deliberately before a stable release.

Install the exact alpha when reproducibility matters:

```sh
pnpm add @astilba/env@0.3.0 --save-exact
```

Version 0.3.0 adds provider-neutral contract inventory export and name-drift checking for process targets. It adds no provider client, value sync, provider-kind evidence, JavaScript package export, browser API, runtime API, or generated-module protocol. Version 0.2.3 added complete TSDoc coverage and the executable adoption-example suite.

## Supported public surface

| Surface | 0.3.0 status |
| --- | --- |
| `defineEnvironment` and `env` builders | Public on supported Node.js releases |
| `astilba-env generate` and `generate --check` | Public on supported Node.js releases |
| `astilba-env check --target ID` | Public for targets the CLI can validate |
| `astilba-env inventory export` and `inventory check` | Public for provider-neutral process-target name evidence |
| `astilba-env plan --base GIT_REF` | Public on supported Node.js releases |
| Generated Node.js `check` and `load` functions | Public |
| Generated Cloudflare Workers deployment targets | Public with first-party codecs; see the narrow runtime boundary below |
| Generated public browser projections | Public |
| Inert same-origin JSON browser protocol | Public |
| Vite private-module boundary | Public for Vite 8.1.5 or later within Vite 8 |
| Next.js App and Pages Router wiring | Application-owned integration; no Astilba adapter |
| Hosted configuration service | Not provided |
| Secret storage or provider provisioning | Not provided |
| Stable API compatibility | Not promised during the public alpha |

## Runtime matrix

| Operation | Node.js | Browser | Cloudflare Workers |
| --- | --- | --- | --- |
| Import `@astilba/env` and author declarations | Supported | Blocked | Blocked |
| Run the generator, CLI, or planner | Supported | Blocked | Blocked |
| Import a generated server target with first-party codecs | Supported | Blocked | Supported for deployment lifecycle only |
| Import a generated target with `opaque` Standard Schema validators | Supported | Blocked | Not admitted |
| Import `@astilba/env/browser` | Supported for server-side envelope assembly, but not as a Node.js configuration target | Supported | Blocked |
| Import generated public browser modules | Supported for server-side envelope assembly and build tooling | Supported | Blocked |
| Import `@astilba/env/vite` | Supported in Vite configuration | Blocked | Blocked |

The package supports these Node.js ranges:

- Node.js 22.14.0 or later within Node 22;
- Node.js 24 within Node 24; and
- Node.js 26 within Node 26.

The Cloudflare Workers path does not require `nodejs_compat` for Env. It accepts a Wrangler-generated `Env` binding interface without an index signature, reads only the declared binding names, and leaves unrelated capability bindings to application code.

Runtime support is export-specific. Evidence for the generated runtime does not make the declaration builders, CLI, browser runtime, or Vite plugin portable to workerd.

## Executable evidence

Support claims are backed by package-consumer and maintained-example runs rather than inferred from type declarations alone:

| Evidence lane | Exercised versions and boundary |
| --- | --- |
| Node.js and TypeScript | Node.js 22.14.0, 22.23.2, 24.18.1, and 26.5.1; TypeScript 6.0.3 and 7.0.2. |
| Package managers | Clean exact-registry consumers with npm and pnpm. |
| Operating systems | Linux release lanes and a Windows Node.js 22.14.0 package-consumer lane. |
| Vite | Vite 8.1.5 package-consumer evidence for the browser boundary. |
| Next.js | Next.js 15.5.22 and 16.2.12 across App Router static, App Router request, Pages Router static, and Pages Router request modes. |
| Cloudflare Workers | Wrangler 4.115.0 with compatibility date `2026-07-29` and bundled workerd `1.20260722.1`; only the narrow generated deployment-target path documented below is admitted. |
| Portable runtime comparison | Bun 1.3.14 exercises portable generated-runtime equivalence; this is not a declaration-authoring, generator, or CLI support claim. |
| Maintained examples | Exact-registry applications for Node.js, Cloudflare Workers, a Next.js static shell, and Vite. Check each example's lockfile for its admitted package version. |

The maintained Next.js example is an isolated pnpm application and deliberately invokes Next's webpack builder because default Turbopack cannot resolve the exact-registry dependency from that repository fixture layout. The package-consumer matrix also passes a default `next build`; do not infer a general Env or Turbopack incompatibility from the example command.

## Package boundaries

| Import or command | Responsibility |
| --- | --- |
| `@astilba/env` | Declaration builders on Node.js |
| `@astilba/env/runtime` | Runtime operations used by generated server targets; exposed to admitted Node.js and workerd consumers |
| `@astilba/env/browser` | Public browser bootstrap loading and validation |
| `@astilba/env/vite` | Node.js Vite boundary that rejects private Env modules from browser graphs |
| `astilba-env` | Node.js command-line interface |

There is no `@astilba/env/next` export. Next.js support uses the same generated modules and browser protocol as another framework.

## Cloudflare Workers boundary

Env's admitted Workers surface is deliberately limited to:

- a generated server target;
- the `deployment` lifecycle;
- first-party Env codecs;
- a direct `check(env)` or `load(env)` call inside the handler; and
- application-owned Wrangler `vars`, `secrets.required`, secret values, and capability bindings.

Request-lifecycle targets and arbitrary Standard Schema validator graphs are not part of the Workers support claim. Env does not inspect live bindings, store secrets, call provider APIs, provision resources, or plan automatic redeployments.

Read [Cloudflare Workers](/docs/env/cloudflare-workers/) before adopting this path.

## Browser boundary

Browser deployment and request values must arrive as same-origin JSON. The runtime validates:

- response status, redirect state, content type, and size;
- the expected audience origin;
- the bootstrap protocol;
- contract, consumer, lifecycle, and projection identity; and
- the exact generated value projection.

The runtime fetches with `cache: "no-store"`. An application endpoint whose response can vary by request must also send `Cache-Control: private, no-store`.

Env does not inject inline JavaScript, write to `window`, mutate HTML, or choose an application route.

## Alpha boundaries

Plan for these constraints in 0.3:

- generated files are application-owned build artifacts and must be regenerated when the declaration changes;
- public build values selected by a browser consumer require an explicit build source during generation;
- custom Standard Schema validation is available only for private server `opaque` entries;
- browser projections use Env's portable built-in codecs;
- compatibility plans contain descriptors and change classifications, never configuration values;
- diagnostics are deliberately redacted; and
- framework-specific startup, routing, authentication, and failure UI remain application responsibilities.

The inventory CLI is also deliberately narrow. It compares declared process-target names with a strict application-supplied name list. It does not query providers, inspect values or provider kinds, infer closed ownership, sync or prune configuration, or replace runtime `check` and `load` validation.

The public [Env repository](https://github.com/astilbahq/env) is the source and issue tracker for the alpha. Report a contract, generation, runtime, or browser-boundary defect there.
