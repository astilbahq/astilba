# Astilba Env

Astilba Env is a local-first configuration contract compiler for TypeScript. You declare which application artifacts need configuration, when each value is resolved, and where it may be exposed; Env generates typed, physically separated interfaces and value-free compatibility evidence.

Env does not replace a secret manager or `.env` files. It does not receive, store, or transmit configuration values; compilation, checking, and planning run in your project.

## Install

```sh
pnpm add @astilba/env
```

The declaration builders, generator, and CLI require Node.js `22.14.0` or later within the published major-version ranges. Generated deployment-lifecycle server targets using Astilba's built-in codecs can also use `@astilba/env/runtime` in Cloudflare Workers without the `nodejs_compat` compatibility flag. Workers refuse build-lifecycle, request-lifecycle, and opaque-schema target execution. Env adds no compatibility-date floor; use the latest date supported by your installed Wrangler. The archive admission lane pins Wrangler `4.115.0` at its supported `2026-07-29` date and observes its bundled workerd `1.20260722.1`; this is singleton admission evidence, not a wider compatibility claim. Vite support is optional and supports Vite versions from `8.1.5` up to, but not including, `9.0.0`.

## Start with one declaration

Create `astilba.env.ts` in an ESM package:

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
      apiOrigin: "PUBLIC_API_ORIGIN",
    }),
    serverDeployment: env.process("server", {
      databaseUrl: "DATABASE_URL",
    }),
  },
});
```

Generate the project-owned interfaces, then make drift checking a required CI step:

```sh
astilba-env generate
astilba-env generate --check
```

Generated server modules expose typed `check` and `load` operations. Generated browser modules expose only the selected public projection; private names, codecs, bindings, values, and full-contract metadata do not enter the browser graph.

## Inventory and name drift

The CLI can compile a deterministic, value-free name inventory for one process target:

```sh
astilba-env inventory export --target serverDeployment
```

The document contains logical entry IDs, bound names, lifecycle, visibility, and required presence. It contains no configuration values and makes no provider-kind claim. Without `--json`, the command writes the canonical `astilba.env.contract-inventory/v1` document to stdout; `--json` wraps it in the versioned CLI response.

Provider tooling stays outside Env. Convert a provider's name-only list into the strict observed form before checking it:

```json
{
  "entries": [{ "name": "DATABASE_URL" }],
  "format": "astilba.env.observed-name-inventory/v1"
}
```

```sh
astilba-env inventory check \
  --target serverDeployment \
  --observed ./observed-names.json \
  --ownership closed
```

`open` ownership is the default; unexpected names are reported without failing. `closed` ownership is always explicit and makes unexpected names fail. A missing required name fails in either mode; a missing optional name is reported without failing. Exit `0` means the name inventory is acceptable, exit `1` means drift or invalid evidence, and exit `2` is reserved for command-line misuse.

Inventory checks prove name presence only. They do not inspect values, verify that a provider stored a value as a secret, move configuration, infer namespace ownership, or turn `required: false` into warning severity. Keep provider conversion, routing, sync, prune, warning policy, and all values application-owned. Bound names remain sensitive operational metadata; do not upload inventory files as CI artefacts by default.

## Package boundaries

- `@astilba/env` provides declaration builders;
- `@astilba/env/runtime` supports generated Node target modules and generated Cloudflare Workers deployment targets using built-in codecs;
- `@astilba/env/browser` validates public browser bootstrap data;
- `@astilba/env/vite` rejects private Env modules from browser graphs;
- `astilba-env` is the Node command-line interface.

Applications own their configuration endpoint and response headers. Responses that vary by request must use `Cache-Control: private, no-store`; the browser runtime also fetches with `cache: "no-store"`.

There is no hosted control plane and no framework-specific semantic layer. Next.js integration is application-owned wiring around generated modules and the browser protocol; there is no `@astilba/env/next` export.

## Support and migration

Read the [public documentation](https://astilba.com/docs/env/) for the supported release boundary and the [migration guide](https://astilba.com/docs/env/migrate-from-next-dynamic-env/) before replacing `next-dynamic-env`. The guide names intentional compatibility changes, including validation and browser-delivery differences.

## Executable examples

The [examples workspace](./examples) contains small, independent Node, Cloudflare Worker, Next.js, and Vite applications. Each installs the released npm package rather than this checkout. From `examples`, run `pnpm install --frozen-lockfile`, then `pnpm verify:all` to regenerate-check and exercise them.

## Security

Please report vulnerabilities through the [security policy](https://github.com/astilbahq/env/security/policy). Do not include secrets or configuration values in a public issue.

## Licence

MIT
