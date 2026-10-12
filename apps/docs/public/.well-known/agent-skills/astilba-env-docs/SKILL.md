---
name: "astilba-env-docs"
description: "Consult Astilba's public Env documentation to integrate the 0.3 configuration contract compiler without weakening browser, server, platform, inventory, or lifecycle boundaries."
---

# Astilba Env documentation

Use this skill when a question concerns `@astilba/env`, its declaration builders, generated modules, lifecycle model, runtime and platform boundaries, provider-neutral name inventory, browser bootstrap protocol, framework wiring, or migration from `next-dynamic-env`.

## Read the public sources

1. When the client supports remote Streamable HTTP MCP, connect to the public, read-only endpoint at `https://astilba.com/docs/mcp`. Use `search_docs` to find focused pages and read the linked Markdown resources; `read_doc` provides bounded chunks when the client does not expose resources directly.
2. Otherwise, start with the [Env document set](https://astilba.com/docs/_llms-txt/astilba-env.txt) when the question spans several topics.
3. When the question is focused, follow the relevant canonical page link in that document set and use the page-specific Markdown alternate it advertises.
4. Use [Configure a Node application](https://astilba.com/docs/env/quickstart.md) for a first integration.
5. Use [Node.js](https://astilba.com/docs/env/nodejs.md), [Browser](https://astilba.com/docs/env/browser-runtime.md), and [Cloudflare Workers](https://astilba.com/docs/env/cloudflare-workers.md) for exact runtime or platform boundaries.
6. Use [Vite](https://astilba.com/docs/env/vite.md) and [Next.js](https://astilba.com/docs/env/nextjs.md) for framework wiring.
7. Use [Lifecycles and projections](https://astilba.com/docs/env/lifecycles-and-projections.md) to decide when and where a value may be resolved.
8. Use [Validation and Standard Schema](https://astilba.com/docs/env/validation-and-standard-schema.md) to choose built-in or opaque validation.
9. Use [Declaration reference](https://astilba.com/docs/env/declaration-reference.md) for builders, codecs, consumers, targets, and rules.
10. Use [Deliver browser configuration](https://astilba.com/docs/env/browser-delivery.md) for endpoint, loader, build-value, and failure guidance.
11. Use [Check name inventory drift](https://astilba.com/docs/env/inventory-and-drift.md) for expected and observed formats, open or closed ownership, issue codes, and provider boundaries.
12. Use [CLI reference](https://astilba.com/docs/env/cli-reference.md) for generation, target checking, inventory, planning, machine formats, and exit statuses.
13. Check [Release and support](https://astilba.com/docs/env/release-and-support.md) before claiming a package, runtime, framework adapter, or compatibility guarantee is supported.
14. Use [Migrate from next-dynamic-env](https://astilba.com/docs/env/migrate-from-next-dynamic-env.md) for legacy API, validation, and intentional non-compatibility guidance.

## Keep the public-alpha boundary explicit

- Treat `@astilba/env` 0.3.0 as a public alpha, not a stable compatibility promise.
- Keep browser and server generated modules physically separate.
- Limit Cloudflare Workers claims to generated deployment targets with first-party codecs. Do not extend that support to request targets, opaque schemas, or other package exports.
- Do not invent a hosted control plane, provider API, secret store, `.env` loader, inline-script transport, or `@astilba/env/next` export.
- Treat inventory as value-free name evidence only. Do not claim it queries providers, verifies provider kinds or values, infers namespace ownership, syncs or prunes configuration, or replaces runtime validation.
- Treat framework routing, startup, endpoint authentication, and response headers as application responsibilities.
- Use inert same-origin JSON for browser deployment and request values; responses that vary by request require `Cache-Control: private, no-store`.

## Answer from evidence

- Prefer the narrowest public page that directly supports the answer.
- Use the canonical URL in each Markdown file's frontmatter when citing a page.
- Distinguish build, deployment, and request values.
- Distinguish generated server targets from generated public browser projections.
- Say when the public docs do not establish an answer. Do not convert alpha intentions into stable guarantees.
